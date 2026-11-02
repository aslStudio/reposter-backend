package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"reposter/internal/store"
	"reposter/internal/telegram"
)

type Rewriter interface {
	Rewrite(ctx context.Context, sourceText, style string) (string, error)
}

type Service struct {
	Store                   *store.Store
	Reader                  telegram.Reader
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
		if err = s.pollSource(ctx, source); err != nil {
			s.Logger.Error("source poll failed", "source_id", source.ID, "error", err)
			failures = append(failures, fmt.Errorf("source %d: %w", source.ID, err))
		}
	}
	return errors.Join(failures...)
}
func (s *Service) pollSource(ctx context.Context, source store.Source) error {
	lastPostID := int64(0)
	if source.Cursor != "" {
		parsed, err := strconv.ParseInt(source.Cursor, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid Telegram source cursor: %w", err)
		}
		lastPostID = parsed
	}
	posts, err := s.Reader.ReadSince(ctx, source.Handle, lastPostID)
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		return nil
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].ID < posts[j].ID })
	if lastPostID == 0 {
		if !s.ReplayLatestOnFirstPoll {
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
		media := make([]string, 0, len(p.ImageURLs))
		for _, imageURL := range p.ImageURLs {
			if len(media) == 10 {
				break
			}
			path, err := s.Media.Save(ctx, imageURL)
			if err != nil {
				return fmt.Errorf("save image for post %d: %w", p.ID, err)
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
			if p.Text == "" {
				continue
			}
			body, err := s.Rewriter.Rewrite(ctx, p.Text, route.Destination.StylePrompt)
			if err != nil {
				return fmt.Errorf("rewrite post %d for destination %d: %w", p.ID, route.Destination.ID, err)
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
	if s.Bot.Token == "" {
		return nil
	}
	var failures []error
	for i := 0; i < 100; i++ {
		draft, dest, ok, err := s.Store.ClaimReady(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		externalID, publishErr := s.Bot.Publish(ctx, dest.ChatID, draft.Body, draft.MediaPaths, telegram.PublishOptions{FormattingEnabled: dest.FormattingEnabled, SignatureText: dest.SignatureText})
		if err = s.Store.FinishPublish(ctx, draft.ID, externalID, publishErr); err != nil {
			return err
		}
		if publishErr != nil {
			s.Logger.Error("publish failed", "draft_id", draft.ID, "error", publishErr)
			failures = append(failures, fmt.Errorf("draft %d: %w", draft.ID, publishErr))
		}
	}
	return errors.Join(failures...)
}
