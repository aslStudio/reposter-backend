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
	"reposter/internal/locale"
	"reposter/internal/rewrite"
	"reposter/internal/store"
	"reposter/internal/telegram"
	"reposter/internal/threads"
	"reposter/internal/x"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL := os.Getenv("DATABASE_URL")
	adminToken := os.Getenv("ADMIN_TOKEN")
	if databaseURL == "" || adminToken == "" {
		logger.Error("DATABASE_URL and ADMIN_TOKEN are required")
		os.Exit(1)
	}
	var panelAuth *httpapi.Authenticator
	panelPassword := os.Getenv("PANEL_PASSWORD")
	panelKey := os.Getenv("PANEL_SESSION_KEY")
	if panelPassword != "" || panelKey != "" || os.Getenv("PANEL_AUTH_REQUIRED") == "true" {
		if panelPassword == "" || panelKey == "" {
			logger.Error("PANEL_PASSWORD and PANEL_SESSION_KEY are both required")
			os.Exit(1)
		}
		var authErr error
		panelAuth, authErr = httpapi.NewAuthenticator(panelPassword, panelKey)
		if authErr != nil {
			logger.Error("invalid panel authentication configuration", "error", authErr)
			os.Exit(1)
		}
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
	if err = bootstrapX(ctx, db); err != nil {
		logger.Error("X bootstrap failed", "error", err)
		os.Exit(1)
	}
	if err = bootstrapThreads(ctx, db); err != nil {
		logger.Error("Threads bootstrap failed", "error", err)
		os.Exit(1)
	}
	mediaDir := os.Getenv("MEDIA_DIR")
	if mediaDir == "" {
		mediaDir = "./data/media"
	}
	var tokenCipher x.TokenCipher
	if encoded := os.Getenv("X_TOKEN_ENCRYPTION_KEY"); encoded != "" {
		tokenCipher, err = x.NewTokenCipher(encoded)
		if err != nil {
			logger.Error("invalid X token encryption key", "error", err)
			os.Exit(1)
		}
	}
	service := &app.Service{Store: db, Reader: telegram.Reader{}, X: x.Client{BearerToken: os.Getenv("X_BEARER_TOKEN"), ConsumerKey: os.Getenv("X_CONSUMER_KEY"), ConsumerSecret: os.Getenv("X_SECRET_KEY"), AccessToken: os.Getenv("X_ACCESS_TOKEN"), AccessSecret: os.Getenv("X_ACCESS_TOKEN_SECRET")}, Threads: threads.Client{AccessToken: os.Getenv("THREADS_ACCESS_TOKEN")}, XTokenCipher: tokenCipher, Media: telegram.MediaStore{Dir: mediaDir}, Rewriter: rewrite.Codex{Model: os.Getenv("CODEX_MODEL"), APIKey: os.Getenv("CODEX_API_KEY")}, Bot: telegram.Bot{Token: os.Getenv("TELEGRAM_BOT_TOKEN")}, Logger: logger, ReplayLatestOnFirstPoll: os.Getenv("REPLAY_LATEST_ON_FIRST_POLL") == "true"}
	switch os.Getenv("THREADS_SOURCE_MODE") {
	case "", "api":
	case "web":
		service.ThreadsSource = threads.WebReader{Cookie: os.Getenv("THREADS_SOURCE_COOKIE")}
	default:
		logger.Error("invalid THREADS_SOURCE_MODE; expected api or web")
		os.Exit(1)
	}
	if raw := strings.TrimSpace(os.Getenv("ACTIVE_PLATFORMS")); raw != "" {
		service.EnabledPlatforms = map[string]bool{}
		for _, platform := range strings.Split(raw, ",") {
			platform = strings.TrimSpace(platform)
			if platform != "telegram" && platform != "x" && platform != "threads" {
				logger.Error("invalid ACTIVE_PLATFORMS", "platform", platform)
				os.Exit(1)
			}
			service.EnabledPlatforms[platform] = true
		}
	}
	logger.Info("worker configured", "active_platforms", os.Getenv("ACTIVE_PLATFORMS"), "threads_source_mode", os.Getenv("THREADS_SOURCE_MODE"))
	interval := time.Minute
	if raw := os.Getenv("POLL_INTERVAL_SECONDS"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n >= 30 {
			interval = time.Duration(n) * time.Second
		}
	}
	runCycle := func() {
		workCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
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
	server := &http.Server{Addr: addr, Handler: httpapi.API{Store: db, Service: service, AdminToken: adminToken, Auth: panelAuth}.Handler(), ReadHeaderTimeout: 5 * time.Second}
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

func bootstrapX(ctx context.Context, db *store.Store) error {
	sourcesRaw := strings.TrimSpace(os.Getenv("BOOTSTRAP_X_SOURCES"))
	destinationRaw := strings.TrimSpace(os.Getenv("BOOTSTRAP_X_DESTINATION"))
	if sourcesRaw == "" && destinationRaw == "" {
		return nil
	}
	if sourcesRaw == "" || destinationRaw == "" {
		return errors.New("both BOOTSTRAP_X_SOURCES and BOOTSTRAP_X_DESTINATION are required")
	}
	destHandle, err := x.ParseHandle(destinationRaw)
	if err != nil {
		return err
	}
	style := strings.TrimSpace(os.Getenv("BOOTSTRAP_X_STYLE"))
	if style == "" {
		return errors.New("BOOTSTRAP_X_STYLE is required")
	}
	languageCode := ""
	if raw := os.Getenv("BOOTSTRAP_X_LANGUAGE"); raw != "" {
		languageCode, err = locale.Normalize(raw)
		if err != nil {
			return errors.New("BOOTSTRAP_X_LANGUAGE must be a valid BCP 47 tag")
		}
	}
	mode := os.Getenv("BOOTSTRAP_X_MODE")
	if mode != "manual_approval" && mode != "automatic" {
		return errors.New("BOOTSTRAP_X_MODE must be manual_approval or automatic")
	}
	limit, err := strconv.Atoi(os.Getenv("BOOTSTRAP_X_DAILY_LIMIT"))
	if err != nil || limit < 0 || limit > 100 {
		return errors.New("BOOTSTRAP_X_DAILY_LIMIT must be 0..100")
	}
	dest, err := db.AddDestination(ctx, store.Destination{Platform: "x", ChatID: "@" + destHandle, StylePrompt: style, Language: languageCode, Mode: mode, DailyLimit: limit})
	if err != nil {
		return err
	}
	for _, raw := range strings.Split(sourcesRaw, ",") {
		handle, err := x.ParseHandle(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		source, err := db.AddPlatformSource(ctx, "x", handle)
		if err != nil {
			return err
		}
		if _, err = db.AddRoute(ctx, source.ID, dest.ID); err != nil {
			return err
		}
	}
	return nil
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
	languageCode := ""
	if raw := os.Getenv("BOOTSTRAP_TG_LANGUAGE"); raw != "" {
		languageCode, err = locale.Normalize(raw)
		if err != nil {
			return errors.New("BOOTSTRAP_TG_LANGUAGE must be a valid BCP 47 tag")
		}
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
	dest, err := db.AddDestination(ctx, store.Destination{ChatID: "@" + destHandle, StylePrompt: style, Language: languageCode, Mode: mode, DailyLimit: limit, FormattingEnabled: formattingEnabled, SignatureText: signatureText})
	if err != nil {
		return err
	}
	_, err = db.AddRoute(ctx, source.ID, dest.ID)
	return err
}

func bootstrapThreads(ctx context.Context, db *store.Store) error {
	sourceRaw := strings.TrimSpace(os.Getenv("BOOTSTRAP_THREADS_SOURCE"))
	destinationRaw := strings.TrimSpace(os.Getenv("BOOTSTRAP_THREADS_DESTINATION"))
	if sourceRaw == "" && destinationRaw == "" {
		return nil
	}
	if sourceRaw == "" || destinationRaw == "" {
		return errors.New("both BOOTSTRAP_THREADS_SOURCE and BOOTSTRAP_THREADS_DESTINATION are required")
	}
	sourceHandle, err := threads.ParseHandle(sourceRaw)
	if err != nil {
		return err
	}
	destHandle, err := threads.ParseHandle(destinationRaw)
	if err != nil {
		return err
	}
	style := strings.TrimSpace(os.Getenv("BOOTSTRAP_THREADS_STYLE"))
	if style == "" {
		return errors.New("BOOTSTRAP_THREADS_STYLE is required")
	}
	languageCode := ""
	if raw := os.Getenv("BOOTSTRAP_THREADS_LANGUAGE"); raw != "" {
		languageCode, err = locale.Normalize(raw)
		if err != nil {
			return errors.New("BOOTSTRAP_THREADS_LANGUAGE must be a valid BCP 47 tag")
		}
	}
	mode := os.Getenv("BOOTSTRAP_THREADS_MODE")
	if mode != "manual_approval" && mode != "automatic" {
		return errors.New("BOOTSTRAP_THREADS_MODE must be manual_approval or automatic")
	}
	limit, err := strconv.Atoi(os.Getenv("BOOTSTRAP_THREADS_DAILY_LIMIT"))
	if err != nil || limit < 0 || limit > 100 {
		return errors.New("BOOTSTRAP_THREADS_DAILY_LIMIT must be 0..100")
	}
	source, err := db.AddPlatformSource(ctx, "threads", sourceHandle)
	if err != nil {
		return err
	}
	dest, err := db.AddDestination(ctx, store.Destination{Platform: "threads", ChatID: "@" + destHandle, StylePrompt: style, Language: languageCode, Mode: mode, DailyLimit: limit})
	if err != nil {
		return err
	}
	_, err = db.AddRoute(ctx, source.ID, dest.ID)
	return err
}
