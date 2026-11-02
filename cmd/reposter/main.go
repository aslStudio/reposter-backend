package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"reposter/internal/app"
	"reposter/internal/httpapi"
	"reposter/internal/rewrite"
	"reposter/internal/store"
	"reposter/internal/telegram"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL := os.Getenv("DATABASE_URL")
	adminToken := os.Getenv("ADMIN_TOKEN")
	if databaseURL == "" || adminToken == "" {
		logger.Error("DATABASE_URL and ADMIN_TOKEN are required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var db *store.Store
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		db, err = store.Open(ctx, databaseURL)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	if err = bootstrapTelegram(ctx, db); err != nil {
		logger.Error("Telegram bootstrap failed", "error", err)
		os.Exit(1)
	}
	mediaDir := os.Getenv("MEDIA_DIR")
	if mediaDir == "" {
		mediaDir = "./data/media"
	}
	service := &app.Service{Store: db, Reader: telegram.Reader{}, Media: telegram.MediaStore{Dir: mediaDir}, Rewriter: rewrite.Codex{Model: os.Getenv("CODEX_MODEL"), APIKey: os.Getenv("CODEX_API_KEY")}, Bot: telegram.Bot{Token: os.Getenv("TELEGRAM_BOT_TOKEN")}, Logger: logger, ReplayLatestOnFirstPoll: os.Getenv("REPLAY_LATEST_ON_FIRST_POLL") == "true"}
	interval := time.Minute
	if raw := os.Getenv("POLL_INTERVAL_SECONDS"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n >= 30 {
			interval = time.Duration(n) * time.Second
		}
	}
	runCycle := func() {
		workCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if err := service.Poll(workCtx); err != nil {
			logger.Error("poll cycle failed", "error", err)
		}
		if err := service.PublishReady(workCtx); err != nil {
			logger.Error("publish cycle failed", "error", err)
		}
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		runCycle()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCycle()
			}
		}
	}()
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: httpapi.API{Store: db, Service: service, AdminToken: adminToken}.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("http server listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown failed", "error", err)
	}
}

func bootstrapTelegram(ctx context.Context, db *store.Store) error {
	sourceRaw := os.Getenv("BOOTSTRAP_TG_SOURCE")
	destinationRaw := os.Getenv("BOOTSTRAP_TG_DESTINATION")
	if sourceRaw == "" && destinationRaw == "" {
		return nil
	}
	if sourceRaw == "" || destinationRaw == "" {
		return errors.New("both BOOTSTRAP_TG_SOURCE and BOOTSTRAP_TG_DESTINATION are required")
	}
	sourceHandle, err := telegram.ParseHandle(sourceRaw)
	if err != nil {
		return err
	}
	destHandle, err := telegram.ParseHandle(destinationRaw)
	if err != nil {
		return err
	}
	style := os.Getenv("BOOTSTRAP_TG_STYLE")
	if style == "" {
		return errors.New("BOOTSTRAP_TG_STYLE is required")
	}
	mode := os.Getenv("BOOTSTRAP_TG_MODE")
	if mode != "manual_approval" && mode != "automatic" {
		return errors.New("BOOTSTRAP_TG_MODE must be manual_approval or automatic")
	}
	limit, err := strconv.Atoi(os.Getenv("BOOTSTRAP_TG_DAILY_LIMIT"))
	if err != nil || limit < 0 || limit > 100 {
		return errors.New("BOOTSTRAP_TG_DAILY_LIMIT must be 0..100 (0 means unlimited)")
	}
	formattingEnabled := false
	if raw := os.Getenv("BOOTSTRAP_TG_FORMATTING_ENABLED"); raw != "" {
		formattingEnabled, err = strconv.ParseBool(raw)
		if err != nil {
			return errors.New("BOOTSTRAP_TG_FORMATTING_ENABLED must be true or false")
		}
	}
	signatureText := strings.TrimSpace(os.Getenv("BOOTSTRAP_TG_SIGNATURE_TEXT"))
	if utf8.RuneCountInString(signatureText) > 200 || strings.ContainsAny(signatureText, "\r\n") {
		return errors.New("BOOTSTRAP_TG_SIGNATURE_TEXT must be one line of at most 200 characters")
	}
	source, err := db.AddSource(ctx, sourceHandle)
	if err != nil {
		return err
	}
	dest, err := db.AddDestination(ctx, store.Destination{ChatID: "@" + destHandle, StylePrompt: style, Mode: mode, DailyLimit: limit, FormattingEnabled: formattingEnabled, SignatureText: signatureText})
	if err != nil {
		return err
	}
	_, err = db.AddRoute(ctx, source.ID, dest.ID)
	return err
}
