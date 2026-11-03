package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"reposter/internal/store"
	"reposter/internal/telegram"
	"reposter/internal/threads"
	"reposter/internal/x"
)

type Rewriter interface {
	Rewrite(ctx context.Context, sourceText, style, language string) (string, error)
}

type ThreadsSourceReader interface {
	ReadSince(ctx context.Context, handle string, afterID int64) ([]telegram.PublicPost, error)
}

type Service struct {
	Store                   *store.Store
	Reader                  telegram.Reader
	X                       x.Client
	Threads                 threads.Client
	ThreadsSource           ThreadsSourceReader
	XTokenCipher            x.TokenCipher
	Media                   telegram.MediaStore
	Rewriter                Rewriter
	Bot                     telegram.Bot
	Logger                  *slog.Logger
	ReplayLatestOnFirstPoll bool
	EnabledPlatforms        map[string]bool
}

func (s *Service) platformEnabled(platform string) bool {
	return s.EnabledPlatforms == nil || s.EnabledPlatforms[platform]
}

func (s *Service) Poll(ctx context.Context) error {
	return s.poll(ctx, false)
}

func (s *Service) PollNow(ctx context.Context) error {
	return s.poll(ctx, true)
}

func (s *Service) poll(ctx context.Context, bypassInterval bool) error {
	if enabled, err := s.Store.AutomationEnabled(ctx); err != nil {
		return err
	} else if !enabled {
		return nil
	}
	sources, err := s.Store.Sources(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, source := range sources {
		if enabled, err := s.Store.AutomationEnabled(ctx); err != nil {
			return err
		} else if !enabled {
			break
		}
		if !source.Enabled || !s.platformEnabled(source.Platform) {
			continue
		}
		if !bypassInterval && (source.Platform == "x" || source.Platform == "threads") && source.LastPolledAt != nil && time.Since(*source.LastPolledAt) < 5*time.Minute {
			continue
		}
		s.Logger.Info("source poll started", "source_id", source.ID, "platform", source.Platform, "handle", source.Handle)
		pollErr := s.pollSource(ctx, source)
		// A failed Threads request still counts as a polling attempt. Keep its
		// cursor unchanged, but wait before retrying permission and API errors.
		if source.Platform == "threads" || (source.Platform == "x" && pollErr == nil) {
			if err = s.Store.MarkPolled(ctx, source.ID); err != nil {
				return err
			}
		}
		if pollErr != nil {
			s.Logger.Error("source poll failed", "source_id", source.ID, "error", pollErr)
			failures = append(failures, fmt.Errorf("source %d: %w", source.ID, pollErr))
		} else {
			s.Logger.Info("source poll completed", "source_id", source.ID)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) pollSource(ctx context.Context, source store.Source) error {
	lastPostID := int64(0)
	if source.Cursor != "" {
		parsed, err := strconv.ParseInt(source.Cursor, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid source cursor: %w", err)
		}
		lastPostID = parsed
	}
	var posts []telegram.PublicPost
	var err error
	switch source.Platform {
	case "telegram":
		posts, err = s.Reader.ReadSince(ctx, source.Handle, lastPostID)
	case "x":
		userID := source.ExternalUserID
		if userID == "" {
			userID, err = s.X.ResolveUser(ctx, source.Handle)
			if err != nil {
				return err
			}
			if err = s.Store.SetXUserID(ctx, source.ID, userID); err != nil {
				return err
			}
		}
		posts, err = s.X.ReadSince(ctx, source.Handle, userID, lastPostID)
	case "threads":
		if s.ThreadsSource != nil {
			posts, err = s.ThreadsSource.ReadSince(ctx, source.Handle, lastPostID)
		} else {
			posts, err = s.Threads.ReadSince(ctx, source.Handle, lastPostID)
		}
	default:
		return fmt.Errorf("unsupported source platform %q", source.Platform)
	}
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		return nil
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].ID < posts[j].ID })
	if lastPostID == 0 {
		if source.Platform == "x" || (source.Platform == "telegram" && !s.ReplayLatestOnFirstPoll) {
			return s.Store.SetCursor(ctx, source.ID, posts[len(posts)-1].ID)
		}
		posts = posts[len(posts)-1:]
	}
	routes, err := s.Store.Routes(ctx, source.ID)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return s.Store.SetCursor(ctx, source.ID, posts[len(posts)-1].ID)
	}
	for _, p := range posts {
		if p.ID <= lastPostID {
			continue
		}
		items := p.MediaItems
		if len(items) == 0 {
			for _, imageURL := range p.ImageURLs {
				items = append(items, telegram.MediaItem{URL: imageURL, Kind: "IMAGE"})
			}
			for _, videoURL := range p.VideoURLs {
				items = append(items, telegram.MediaItem{URL: videoURL, Kind: "VIDEO"})
			}
		}
		media := make([]string, 0, len(items))
		mediaSourceURLs := make([]string, 0, len(items))
		for _, item := range items {
			if len(media) == 10 {
				return fmt.Errorf("post %d exceeds 10 media items", p.ID)
			}
			var path string
			var err error
			if item.Kind == "VIDEO" {
				path, err = s.Media.SaveVideo(ctx, item.URL)
			} else {
				path, err = s.Media.Save(ctx, item.URL)
			}
			if err != nil {
				return fmt.Errorf("save media for post %d: %w", p.ID, err)
			}
			media = append(media, path)
			mediaSourceURLs = append(mediaSourceURLs, item.URL)
		}
		postID, err := s.Store.SavePost(ctx, source.ID, store.Post{ExternalID: strconv.FormatInt(p.ID, 10), URL: p.URL, Text: p.Text, MediaPaths: media, MediaSourceURLs: mediaSourceURLs, PublishedAt: p.PublishedAt})
		if err != nil {
			return err
		}
		for _, route := range routes {
			exists, err := s.Store.HasDraft(ctx, route.ID, postID)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			if p.Text == "" && len(media) == 0 {
				continue
			}
			style := route.Destination.StylePrompt
			if route.Destination.Platform == "x" {
				style += "\nПубликация для X: максимум 240 символов вместе с заголовком. Не добавляй Markdown, ссылки, хэштеги и подписи."
			} else if route.Destination.Platform == "threads" {
				style += "\nПубликация для Threads: максимум 450 символов. Не добавляй Markdown и чужие подписи."
			}
			body := ""
			if strings.TrimSpace(p.Text) != "" {
				body, err = s.Rewriter.Rewrite(ctx, p.Text, style, route.Destination.Language)
				if err != nil {
					return fmt.Errorf("rewrite post %d for destination %d: %w", p.ID, route.Destination.ID, err)
				}
			}
			if route.Destination.Platform == "x" && (utf8.RuneCountInString(body) > 280 || (strings.TrimSpace(body) == "" && len(media) == 0)) {
				return fmt.Errorf("X rewrite for post %d exceeds 280 characters or is empty without media", p.ID)
			}
			if route.Destination.Platform == "threads" && utf8.RuneCountInString(body) > 500 {
				return fmt.Errorf("Threads rewrite for post %d exceeds 500 characters", p.ID)
			}
			if err = s.Store.AddDraft(ctx, route.ID, postID, route.Destination, body, media, mediaSourceURLs); err != nil {
				return err
			}
		}
		if err = s.Store.SetCursor(ctx, source.ID, p.ID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) PublishReady(ctx context.Context) error {
	var failures []error
	for i := 0; i < 100; i++ {
		if enabled, err := s.Store.AutomationEnabled(ctx); err != nil {
			return err
		} else if !enabled {
			break
		}
		canX := s.platformEnabled("x") && s.X.ConsumerKey != "" && s.X.ConsumerSecret != "" && s.XTokenCipher.Enabled()
		draft, dest, ok, err := s.Store.ClaimReady(ctx, s.platformEnabled("telegram") && s.Bot.Token != "", canX || (s.platformEnabled("x") && s.X.CanPublish()), s.platformEnabled("x") && s.X.CanPublish(), s.platformEnabled("threads") && s.Threads.AccessToken != "")
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		var externalID string
		var publishErr error
		switch dest.Platform {
		case "telegram":
			if s.Bot.Token == "" {
				publishErr = errors.New("TELEGRAM_BOT_TOKEN is not configured")
			} else {
				externalID, publishErr = s.Bot.Publish(ctx, dest.ChatID, draft.Body, draft.MediaPaths, telegram.PublishOptions{FormattingEnabled: dest.FormattingEnabled, SignatureText: dest.SignatureText})
			}
		case "x":
			handle := strings.TrimPrefix(dest.ChatID, "@")
			client, clientErr := s.xClientForDestination(ctx, dest.ID)
			publishErr = clientErr
			if publishErr == nil {
				publishErr = client.VerifyPublisher(ctx, handle)
			}
			if publishErr == nil {
				externalID, publishErr = client.Publish(ctx, draft.Body, draft.MediaPaths)
			}
		case "threads":
			externalID, publishErr = s.Threads.Publish(ctx, strings.TrimPrefix(dest.ChatID, "@"), draft.Body, draft.MediaSourceURLs, draft.MediaPaths)
		default:
			publishErr = fmt.Errorf("unsupported destination platform %q", dest.Platform)
		}
		if err = s.Store.FinishPublish(ctx, draft.ID, externalID, publishErr); err != nil {
			return err
		}
		if dest.Platform == "x" && x.IsOAuthPermissionError(publishErr) {
			if err = s.Store.DisableXPublishing(ctx, dest.ID); err != nil {
				return err
			}
		}
		if publishErr != nil {
			s.Logger.Error("publish failed", "draft_id", draft.ID, "error", publishErr)
			failures = append(failures, fmt.Errorf("draft %d: %w", draft.ID, publishErr))
		} else {
			s.Logger.Info("publish completed", "draft_id", draft.ID, "platform", dest.Platform, "external_id", externalID)
		}
	}
	return errors.Join(failures...)
}
