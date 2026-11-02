package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"reposter/internal/app"
	"reposter/internal/store"
	"reposter/internal/telegram"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(a.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a API) addSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	handle, err := telegram.ParseHandle(body.URL)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	source, err := a.Store.AddSource(r.Context(), handle)
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
	if !validChatID(body.ChatID) || strings.TrimSpace(body.StylePrompt) == "" || len(body.StylePrompt) > 5000 ||
		(body.Mode != "manual_approval" && body.Mode != "automatic") || body.DailyLimit < 0 || body.DailyLimit > 100 ||
		utf8.RuneCountInString(body.SignatureText) > 200 || strings.ContainsAny(body.SignatureText, "\r\n") ||
		(body.SignatureText != "" && !strings.HasPrefix(body.ChatID, "@")) {
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
	if err = a.Store.RetryRejectedMembership(r.Context(), id); err != nil {
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
