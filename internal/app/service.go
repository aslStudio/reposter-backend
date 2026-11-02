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
	"reposter/internal/x"
)

type Rewriter interface {
	Rewrite(ctx context.Context, sourceText, style, language string) (string, error)
}

type Service struct {
	Store                   *store.Store
	Reader                  telegram.Reader
	X                       x.Client
	XTokenCipher            x.TokenCipher
	Media                   telegram.MediaStore
	Rewriter                Rewriter
	Bot                     telegram.Bot
	Logger                  *slog.Logger
	ReplayLatestOnFirstPoll bool
}

func (s *Service) Poll(ctx context.Context) error {
	sources, err := s.Store.Sources(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, source := range sources {
		if source.Platform == "x" && source.LastPolledAt != nil && time.Since(*source.LastPolledAt) < 5*time.Minute {
			continue
		}
		if err = s.pollSource(ctx, source); err != nil {
			s.Logger.Error("source poll failed", "source_id", source.ID, "error", err)
			failures = append(failures, fmt.Errorf("source %d: %w", source.ID, err))
		} else if source.Platform == "x" {
			if err = s.Store.MarkPolled(ctx, source.ID); err != nil {
				return err
			}
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
		if source.Platform == "x" || !s.ReplayLatestOnFirstPoll {
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
		media := make([]string, 0, len(p.ImageURLs)+len(p.VideoURLs))
		for _, imageURL := range p.ImageURLs {
			if len(media) == 10 {
				return fmt.Errorf("post %d exceeds 10 media items", p.ID)
			}
			path, err := s.Media.Save(ctx, imageURL)
			if err != nil {
				return fmt.Errorf("save image for post %d: %w", p.ID, err)
			}
			media = append(media, path)
		}
		for _, videoURL := range p.VideoURLs {
			if len(media) == 10 {
				return fmt.Errorf("post %d exceeds 10 media items", p.ID)
			}
			path, err := s.Media.SaveVideo(ctx, videoURL)
			if err != nil {
				return fmt.Errorf("save video for post %d: %w", p.ID, err)
			}
			media = append(media, path)
		}
		postID, err := s.Store.SavePost(ctx, source.ID, store.Post{ExternalID: strconv.FormatInt(p.ID, 10), URL: p.URL, Text: p.Text, MediaPaths: media, PublishedAt: p.PublishedAt})
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
			if err = s.Store.AddDraft(ctx, route.ID, postID, route.Destination, body, media); err != nil {
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
		canX := s.X.ConsumerKey != "" && s.X.ConsumerSecret != "" && s.XTokenCipher.Enabled()
		draft, dest, ok, err := s.Store.ClaimReady(ctx, s.Bot.Token != "", canX || s.X.CanPublish(), s.X.CanPublish())
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
		}
	}
	return errors.Join(failures...)
}
