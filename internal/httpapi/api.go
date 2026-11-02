package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"reposter/internal/app"
	"reposter/internal/locale"
	"reposter/internal/store"
	"reposter/internal/telegram"
	"reposter/internal/x"
)

type API struct {
	Store      *store.Store
	Service    *app.Service
	AdminToken string
}

func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.DB.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/sources", a.addSource)
	mux.HandleFunc("GET /v1/sources", a.listSources)
	mux.HandleFunc("POST /v1/destinations", a.addDestination)
	mux.HandleFunc("GET /v1/destinations", a.listDestinations)
	mux.HandleFunc("POST /v1/routes", a.addRoute)
	mux.HandleFunc("GET /v1/drafts", a.listDrafts)
	mux.HandleFunc("GET /v1/drafts/{id}/media/{index}", a.draftMedia)
	mux.HandleFunc("POST /v1/drafts/{id}/approve", a.approve)
	mux.HandleFunc("POST /v1/drafts/{id}/retry", a.retry)
	mux.HandleFunc("POST /v1/poll", a.poll)
	mux.HandleFunc("POST /v1/x/connections/start", a.xConnectionStart)
	mux.HandleFunc("POST /v1/x/connections/complete", a.xConnectionComplete)
	mux.HandleFunc("POST /v1/x/destinations/{id}/test-post", a.xTestPost)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(a.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a API) xConnectionStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DestinationID int64 `json:"destination_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.DestinationID < 1 {
		writeError(w, 400, "invalid destination_id")
		return
	}
	token, authURL, err := a.Service.XConnectionStart(r.Context(), body.DestinationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 400, "X destination not found")
		return
	}
	if err != nil {
		writeError(w, 502, "X authorization start failed; check server logs")
		a.Service.Logger.Error("X authorization start failed", "error", err)
		return
	}
	writeJSON(w, 200, map[string]string{"request_token": token, "authorization_url": authURL})
}
func (a API) xConnectionComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DestinationID int64  `json:"destination_id"`
		RequestToken  string `json:"request_token"`
		PIN           string `json:"pin"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.DestinationID < 1 || len(body.RequestToken) > 256 || body.RequestToken == "" || len(body.PIN) < 4 || len(body.PIN) > 16 {
		writeError(w, 400, "invalid authorization data")
		return
	}
	username, err := a.Service.XConnectionComplete(r.Context(), body.DestinationID, body.RequestToken, body.PIN)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 400, "authorization expired or destination not found")
		return
	}
	if errors.Is(err, app.ErrXAccountMismatch) {
		writeError(w, 409, err.Error())
		return
	}
	if err != nil {
		writeError(w, 502, "X authorization failed; check server logs")
		a.Service.Logger.Error("X authorization failed", "error", err)
		return
	}
	writeJSON(w, 200, map[string]any{"destination_id": body.DestinationID, "username": username, "status": "connected"})
}
func (a API) xTestPost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid destination id")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" || utf8.RuneCountInString(body.Text) > 280 {
		writeError(w, 400, "text must contain 1..280 characters")
		return
	}
	destination, err := a.Store.XDestination(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 400, "X destination not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load X destination")
		return
	}
	postID, err := a.Service.XTestPost(r.Context(), id, body.Text)
	if errors.Is(err, app.ErrXNotConnected) {
		writeError(w, 409, "X destination is not connected")
		return
	}
	if err != nil {
		a.Service.Logger.Error("X test publication failed", "error", err)
		if strings.HasPrefix(err.Error(), "X API HTTP ") {
			writeError(w, 502, err.Error())
		} else {
			writeError(w, 502, "X test publication failed; check server logs")
		}
		return
	}
	writeJSON(w, 200, map[string]string{"id": postID, "url": "https://x.com/" + strings.TrimPrefix(destination.ChatID, "@") + "/status/" + postID})
}
func (a API) addSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		Platform string `json:"platform"`
	}
	if !decode(w, r, &body) {
		return
	}
	platform := body.Platform
	if platform == "" {
		platform = "telegram"
	}
	var handle string
	var err error
	switch platform {
	case "telegram":
		handle, err = telegram.ParseHandle(body.URL)
	case "x":
		handle, err = x.ParseHandle(body.URL)
	default:
		writeError(w, 400, "unsupported source platform")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	source, err := a.Store.AddPlatformSource(r.Context(), platform, handle)
	if err != nil {
		writeError(w, 500, "could not save source")
		return
	}
	writeJSON(w, 201, source)
}
func (a API) listSources(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.Sources(r.Context())
	if err != nil {
		writeError(w, 500, "could not list sources")
		return
	}
	if list == nil {
		list = []store.Source{}
	}
	writeJSON(w, 200, list)
}
func (a API) addDestination(w http.ResponseWriter, r *http.Request) {
	var body store.Destination
	if !decode(w, r, &body) {
		return
	}
	body.SignatureText = strings.TrimSpace(body.SignatureText)
	if body.Language != "" {
		language, err := locale.Normalize(body.Language)
		if err != nil {
			writeError(w, 400, "invalid language; use a BCP 47 tag such as ru, en, or tr")
			return
		}
		body.Language = language
	}
	if body.Platform == "" {
		body.Platform = "telegram"
	}
	if (body.Platform != "telegram" && body.Platform != "x") ||
		(body.Platform == "telegram" && !validChatID(body.ChatID)) ||
		(body.Platform == "x" && !validXDestination(body.ChatID)) ||
		strings.TrimSpace(body.StylePrompt) == "" || len(body.StylePrompt) > 5000 ||
		(body.Mode != "manual_approval" && body.Mode != "automatic") || body.DailyLimit < 0 || body.DailyLimit > 100 ||
		utf8.RuneCountInString(body.SignatureText) > 200 || strings.ContainsAny(body.SignatureText, "\r\n") ||
		(body.SignatureText != "" && (body.Platform != "telegram" || !strings.HasPrefix(body.ChatID, "@"))) ||
		(body.Platform == "x" && body.FormattingEnabled) {
		writeError(w, 400, "invalid chat_id, style_prompt, mode, daily_limit, or signature_text")
		return
	}
	d, err := a.Store.AddDestination(r.Context(), body)
	if err != nil {
		writeError(w, 500, "could not save destination")
		return
	}
	writeJSON(w, 201, d)
}
func validXDestination(id string) bool {
	if !strings.HasPrefix(id, "@") {
		return false
	}
	handle, err := x.ParseHandle(id)
	return err == nil && "@"+handle == strings.ToLower(id)
}

func (a API) listDestinations(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.Destinations(r.Context())
	if err != nil {
		writeError(w, 500, "could not list destinations")
		return
	}
	if list == nil {
		list = []store.Destination{}
	}
	writeJSON(w, 200, list)
}
func validChatID(id string) bool {
	if strings.HasPrefix(id, "@") {
		_, err := telegram.ParseHandle(id)
		return err == nil
	}
	if strings.HasPrefix(id, "-100") {
		_, err := strconv.ParseInt(id, 10, 64)
		return err == nil
	}
	return false
}
func (a API) addRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceID      int64 `json:"source_id"`
		DestinationID int64 `json:"destination_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.SourceID < 1 || body.DestinationID < 1 {
		writeError(w, 400, "invalid source_id or destination_id")
		return
	}
	id, err := a.Store.AddRoute(r.Context(), body.SourceID, body.DestinationID)
	if err != nil {
		writeError(w, 400, "source or destination not found")
		return
	}
	writeJSON(w, 201, map[string]int64{"id": id})
}
func (a API) listDrafts(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "pending_approval" && status != "ready_to_publish" && status != "publishing" && status != "published" && status != "publish_failed" {
		writeError(w, 400, "invalid status")
		return
	}
	list, err := a.Store.ListDrafts(r.Context(), status)
	if err != nil {
		writeError(w, 500, "could not list drafts")
		return
	}
	if list == nil {
		list = []store.Draft{}
	}
	writeJSON(w, 200, list)
}
func (a API) draftMedia(w http.ResponseWriter, r *http.Request) {
	id, errID := strconv.ParseInt(r.PathValue("id"), 10, 64)
	index, errIndex := strconv.Atoi(r.PathValue("index"))
	if errID != nil || errIndex != nil || id < 1 || index < 0 {
		writeError(w, 400, "invalid media path")
		return
	}
	path, err := a.Store.DraftMediaPath(r.Context(), id, index)
	if err != nil {
		writeError(w, 404, "media not found")
		return
	}
	root, err := filepath.Abs(a.Service.Media.Dir)
	if err != nil {
		writeError(w, 500, "media unavailable")
		return
	}
	path, err = filepath.Abs(path)
	if err != nil || filepath.Dir(path) != root {
		writeError(w, 500, "media unavailable")
		return
	}
	http.ServeFile(w, r, path)
}
func (a API) approve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid draft id")
		return
	}
	if err = a.Store.Approve(r.Context(), id); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready_to_publish"})
}
func (a API) retry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid draft id")
		return
	}
	if err = a.Store.RetryDefinitiveRejection(r.Context(), id); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready_to_publish"})
}
func (a API) poll(w http.ResponseWriter, r *http.Request) {
	if err := a.Service.Poll(r.Context()); err != nil {
		writeError(w, 502, "source polling failed; check server logs")
		return
	}
	if err := a.Service.PublishReady(r.Context()); err != nil {
		writeError(w, 502, "publication failed; check drafts and server logs")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON body")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		writeError(w, 400, "multiple JSON values")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
