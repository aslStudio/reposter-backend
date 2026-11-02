package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct{ DB *pgxpool.Pool }

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{DB: pool}, nil
}

func (s *Store) Close() { s.DB.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		var applied bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, file.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", file.Name(), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, file.Name()); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type Source struct {
	ID             int64      `json:"id"`
	Platform       string     `json:"platform"`
	Handle         string     `json:"handle"`
	URL            string     `json:"url"`
	Cursor         string     `json:"-"`
	ExternalUserID string     `json:"-"`
	LastPolledAt   *time.Time `json:"-"`
}
type Destination struct {
	ID                int64  `json:"id"`
	Platform          string `json:"platform"`
	ChatID            string `json:"chat_id"`
	StylePrompt       string `json:"style_prompt"`
	Language          string `json:"language"`
	Mode              string `json:"mode"`
	DailyLimit        int    `json:"daily_limit"`
	FormattingEnabled bool   `json:"formatting_enabled"`
	SignatureText     string `json:"signature_text"`
}
type Route struct {
	ID          int64
	SourceID    int64
	Destination Destination
}
type Post struct {
	ID          int64
	ExternalID  string
	URL         string
	Text        string
	MediaPaths  []string
	PublishedAt time.Time
}
type Draft struct {
	ID             int64     `json:"id"`
	SourceURL      string    `json:"source_url"`
	DestinationID  int64     `json:"destination_id"`
	Body           string    `json:"body"`
	MediaPaths     []string  `json:"-"`
	MediaURLs      []string  `json:"media_urls"`
	Status         string    `json:"status"`
	ExternalPostID string    `json:"external_post_id,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *Store) AddSource(ctx context.Context, handle string) (Source, error) {
	return s.AddPlatformSource(ctx, "telegram", handle)
}
func (s *Store) AddPlatformSource(ctx context.Context, platform, handle string) (Source, error) {
	var out Source
	if platform != "telegram" && platform != "x" {
		return out, errors.New("unsupported source platform")
	}
	url := "https://t.me/s/" + handle
	if platform == "x" {
		url = "https://x.com/" + handle
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO sources(platform, handle, url) VALUES($1,$2,$3)
	 ON CONFLICT(platform,handle) DO UPDATE SET handle=EXCLUDED.handle
	 RETURNING id,platform,handle,url,cursor,external_user_id,last_polled_at`, platform, handle, url).Scan(&out.ID, &out.Platform, &out.Handle, &out.URL, &out.Cursor, &out.ExternalUserID, &out.LastPolledAt)
	return out, err
}
func (s *Store) AddDestination(ctx context.Context, d Destination) (Destination, error) {
	if d.Platform == "" {
		d.Platform = "telegram"
	}
	if d.Platform != "telegram" && d.Platform != "x" {
		return d, errors.New("unsupported destination platform")
	}
	languageSpecified := d.Language != ""
	if !languageSpecified {
		d.Language = "ru"
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO destinations(platform,external_id,style_prompt,language,mode,daily_post_limit,formatting_enabled,signature_text)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8)
	 ON CONFLICT(platform,external_id) DO UPDATE SET style_prompt=EXCLUDED.style_prompt,mode=EXCLUDED.mode,daily_post_limit=EXCLUDED.daily_post_limit,
	 formatting_enabled=EXCLUDED.formatting_enabled,signature_text=EXCLUDED.signature_text,
	 language=CASE WHEN $9 THEN EXCLUDED.language ELSE destinations.language END
	 RETURNING id,language`, d.Platform, d.ChatID, d.StylePrompt, d.Language, d.Mode, d.DailyLimit, d.FormattingEnabled, d.SignatureText, languageSpecified).Scan(&d.ID, &d.Language)
	return d, err
}

func (s *Store) Destinations(ctx context.Context) ([]Destination, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, platform, external_id, style_prompt, language, mode, daily_post_limit, formatting_enabled, signature_text FROM destinations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Destination
	for rows.Next() {
		var d Destination
		if err := rows.Scan(&d.ID, &d.Platform, &d.ChatID, &d.StylePrompt, &d.Language, &d.Mode, &d.DailyLimit, &d.FormattingEnabled, &d.SignatureText); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}
func (s *Store) AddRoute(ctx context.Context, sourceID, destinationID int64) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO routes(source_id,destination_id) VALUES($1,$2)
	 ON CONFLICT(source_id,destination_id) DO UPDATE SET source_id=EXCLUDED.source_id RETURNING id`, sourceID, destinationID).Scan(&id)
	return id, err
}
func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,platform,handle,url,cursor,external_user_id,last_polled_at FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Source
	for rows.Next() {
		var x Source
		if err = rows.Scan(&x.ID, &x.Platform, &x.Handle, &x.URL, &x.Cursor, &x.ExternalUserID, &x.LastPolledAt); err != nil {
			return nil, err
		}
		list = append(list, x)
	}
	return list, rows.Err()
}
func (s *Store) Routes(ctx context.Context, sourceID int64) ([]Route, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.id,r.source_id,d.id,d.platform,d.external_id,d.style_prompt,d.language,d.mode,d.daily_post_limit,d.formatting_enabled,d.signature_text
	 FROM routes r JOIN destinations d ON d.id=r.destination_id WHERE r.source_id=$1 ORDER BY r.id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Route
	for rows.Next() {
		var x Route
		if err = rows.Scan(&x.ID, &x.SourceID, &x.Destination.ID, &x.Destination.Platform, &x.Destination.ChatID, &x.Destination.StylePrompt, &x.Destination.Language, &x.Destination.Mode, &x.Destination.DailyLimit, &x.Destination.FormattingEnabled, &x.Destination.SignatureText); err != nil {
			return nil, err
		}
		list = append(list, x)
	}
	return list, rows.Err()
}
func (s *Store) SavePost(ctx context.Context, sourceID int64, p Post) (int64, error) {
	var id int64
	var publishedAt any
	if !p.PublishedAt.IsZero() {
		publishedAt = p.PublishedAt
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO source_posts(source_id,external_id,original_url,body,media_paths,published_at)
	 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(source_id,external_id) DO UPDATE SET external_id=EXCLUDED.external_id RETURNING id`,
		sourceID, p.ExternalID, p.URL, p.Text, p.MediaPaths, publishedAt).Scan(&id)
	return id, err
}
func (s *Store) HasDraft(ctx context.Context, routeID, postID int64) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM drafts WHERE route_id=$1 AND source_post_id=$2)`, routeID, postID).Scan(&exists)
	return exists, err
}
func (s *Store) AddDraft(ctx context.Context, routeID, postID int64, d Destination, body string, media []string) error {
	status := "pending_approval"
	if d.Mode == "automatic" {
		status = "ready_to_publish"
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO drafts(route_id,source_post_id,destination_id,body,media_paths,status)
	 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(route_id,source_post_id) DO NOTHING`, routeID, postID, d.ID, body, media, status)
	return err
}
func (s *Store) SetCursor(ctx context.Context, sourceID, cursor int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE sources SET cursor=$2 WHERE id=$1 AND CASE WHEN cursor='' THEN true ELSE cursor::bigint < $3 END`, sourceID, strconv.FormatInt(cursor, 10), cursor)
	return err
}
func (s *Store) SetXUserID(ctx context.Context, sourceID int64, userID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE sources SET external_user_id=$2 WHERE id=$1 AND platform='x'`, sourceID, userID)
	return err
}
func (s *Store) MarkPolled(ctx context.Context, sourceID int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE sources SET last_polled_at=now() WHERE id=$1`, sourceID)
	return err
}
func (s *Store) XDestination(ctx context.Context, id int64) (Destination, error) {
	var d Destination
	err := s.DB.QueryRow(ctx, `SELECT id,platform,external_id FROM destinations WHERE id=$1 AND platform='x'`, id).Scan(&d.ID, &d.Platform, &d.ChatID)
	return d, err
}
func (s *Store) SaveXOAuthRequest(ctx context.Context, destinationID int64, token string, secretCipher []byte) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM x_oauth_requests WHERE expires_at<now()`)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO x_oauth_requests(request_token,destination_id,secret_cipher,expires_at)
	 VALUES($1,$2,$3,now()+interval '10 minutes')`, token, destinationID, secretCipher)
	return err
}
func (s *Store) XOAuthRequest(ctx context.Context, destinationID int64, token string) ([]byte, error) {
	var cipher []byte
	err := s.DB.QueryRow(ctx, `SELECT secret_cipher FROM x_oauth_requests WHERE destination_id=$1 AND request_token=$2 AND expires_at>now()`, destinationID, token).Scan(&cipher)
	return cipher, err
}
func (s *Store) SaveXConnection(ctx context.Context, destinationID int64, username string, tokenCipher, secretCipher []byte, requestToken string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO x_connections(destination_id,username,token_cipher,secret_cipher)
	 VALUES($1,$2,$3,$4) ON CONFLICT(destination_id) DO UPDATE SET username=EXCLUDED.username,
	 token_cipher=EXCLUDED.token_cipher,secret_cipher=EXCLUDED.secret_cipher,publish_enabled=true,connected_at=now()`, destinationID, username, tokenCipher, secretCipher)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM x_oauth_requests WHERE request_token=$1 AND destination_id=$2`, requestToken, destinationID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) DisableXPublishing(ctx context.Context, destinationID int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE x_connections SET publish_enabled=false WHERE destination_id=$1`, destinationID)
	return err
}
func (s *Store) XConnection(ctx context.Context, destinationID int64) (string, []byte, []byte, error) {
	var username string
	var tokenCipher, secretCipher []byte
	err := s.DB.QueryRow(ctx, `SELECT username,token_cipher,secret_cipher FROM x_connections WHERE destination_id=$1`, destinationID).Scan(&username, &tokenCipher, &secretCipher)
	return username, tokenCipher, secretCipher, err
}
func (s *Store) ListDrafts(ctx context.Context, status string) ([]Draft, error) {
	query := `SELECT d.id,p.original_url,d.destination_id,d.body,d.media_paths,d.status,COALESCE(d.external_post_id,''),COALESCE(d.error,''),d.created_at
	 FROM drafts d JOIN source_posts p ON p.id=d.source_post_id`
	args := []any{}
	if status != "" {
		query += ` WHERE d.status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY d.created_at DESC LIMIT 100`
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Draft
	for rows.Next() {
		var x Draft
		if err = rows.Scan(&x.ID, &x.SourceURL, &x.DestinationID, &x.Body, &x.MediaPaths, &x.Status, &x.ExternalPostID, &x.Error, &x.CreatedAt); err != nil {
			return nil, err
		}
		x.MediaURLs = make([]string, len(x.MediaPaths))
		for i := range x.MediaPaths {
			x.MediaURLs[i] = fmt.Sprintf("/v1/drafts/%d/media/%d", x.ID, i)
		}
		list = append(list, x)
	}
	return list, rows.Err()
}

func (s *Store) DraftMediaPath(ctx context.Context, id int64, index int) (string, error) {
	var paths []string
	if err := s.DB.QueryRow(ctx, `SELECT media_paths FROM drafts WHERE id=$1`, id).Scan(&paths); err != nil {
		return "", err
	}
	if index < 0 || index >= len(paths) {
		return "", pgx.ErrNoRows
	}
	return paths[index], nil
}
func (s *Store) Approve(ctx context.Context, id int64) error {
	ct, err := s.DB.Exec(ctx, `UPDATE drafts SET status='ready_to_publish' WHERE id=$1 AND status='pending_approval'`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return errors.New("draft not found or not pending approval")
	}
	return nil
}

func (s *Store) RetryDefinitiveRejection(ctx context.Context, id int64) error {
	ct, err := s.DB.Exec(ctx, `UPDATE drafts SET status='ready_to_publish',error=NULL
	 WHERE id=$1 AND status='publish_failed' AND external_post_id IS NULL
	 AND (error='Telegram API HTTP 403: Forbidden: bot is not a member of the channel chat'
	 OR error='X API HTTP 403: Your client app is not configured with the appropriate oauth1 app permissions for this endpoint.')`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return errors.New("draft is not eligible for safe retry")
	}
	return nil
}

// ClaimReady serializes quota checks on the destination row. A publishing draft is
// never automatically retried: the API may have succeeded before a network error.
func (s *Store) ClaimReady(ctx context.Context, allowTelegram, allowX, allowXFallback bool) (Draft, Destination, bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT dr.id,dr.destination_id FROM drafts dr
	 JOIN destinations d ON d.id=dr.destination_id
	 WHERE dr.status='ready_to_publish' AND ((d.platform='telegram' AND $1) OR
	 (d.platform='x' AND $2 AND (($3 AND NOT EXISTS(SELECT 1 FROM x_connections xc WHERE xc.destination_id=d.id))
	 OR EXISTS(SELECT 1 FROM x_connections xc WHERE xc.destination_id=d.id AND xc.publish_enabled))))
	 ORDER BY dr.created_at`, allowTelegram, allowX, allowXFallback)
	if err != nil {
		return Draft{}, Destination{}, false, err
	}
	type pair struct{ id, dest int64 }
	var ids []pair
	for rows.Next() {
		var p pair
		if err = rows.Scan(&p.id, &p.dest); err != nil {
			rows.Close()
			return Draft{}, Destination{}, false, err
		}
		ids = append(ids, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Draft{}, Destination{}, false, err
	}
	for _, p := range ids {
		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return Draft{}, Destination{}, false, err
		}
		var d Destination
		err = tx.QueryRow(ctx, `SELECT id,platform,external_id,style_prompt,language,mode,daily_post_limit,formatting_enabled,signature_text FROM destinations WHERE id=$1 FOR UPDATE`, p.dest).
			Scan(&d.ID, &d.Platform, &d.ChatID, &d.StylePrompt, &d.Language, &d.Mode, &d.DailyLimit, &d.FormattingEnabled, &d.SignatureText)
		if err != nil {
			_ = tx.Rollback(ctx)
			return Draft{}, Destination{}, false, err
		}
		var count int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM drafts WHERE destination_id=$1 AND status IN ('published','publishing','publish_failed')
		 AND (published_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
		 OR (status='publishing' AND created_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
		 OR (status='publish_failed' AND external_post_id IS NOT NULL AND created_at >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'))`, d.ID).Scan(&count)
		if err != nil {
			_ = tx.Rollback(ctx)
			return Draft{}, Destination{}, false, err
		}
		if d.DailyLimit > 0 && count >= d.DailyLimit {
			_ = tx.Rollback(ctx)
			continue
		}
		var draft Draft
		err = tx.QueryRow(ctx, `UPDATE drafts SET status='publishing' WHERE id=$1 AND status='ready_to_publish'
		 RETURNING id,body,media_paths,status,created_at`, p.id).Scan(&draft.ID, &draft.Body, &draft.MediaPaths, &draft.Status, &draft.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return Draft{}, Destination{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return Draft{}, Destination{}, false, err
		}
		draft.DestinationID = d.ID
		return draft, d, true, nil
	}
	return Draft{}, Destination{}, false, nil
}
func (s *Store) FinishPublish(ctx context.Context, id int64, externalID string, sendErr error) error {
	if sendErr != nil {
		_, err := s.DB.Exec(ctx, `UPDATE drafts SET status='publish_failed',error=$2,external_post_id=NULLIF($3,'') WHERE id=$1 AND status='publishing'`, id, sendErr.Error(), externalID)
		return err
	}
	ct, err := s.DB.Exec(ctx, `UPDATE drafts SET status='published',external_post_id=$2,published_at=now(),error=NULL WHERE id=$1 AND status='publishing'`, id, externalID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("draft %d is not publishing", id)
	}
	return nil
}
