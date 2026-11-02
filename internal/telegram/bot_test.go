package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBotSendsFormattedText(t *testing.T) {
	bot := Bot{Token: "test-token", Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Text != "<b>Title</b>\n\nText" || payload.ParseMode != "HTML" {
			t.Fatalf("unexpected payload: %+v", payload)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":123}}`)), Header: make(http.Header)}, nil
	})}}
	if _, err := bot.Publish(context.Background(), "@my_channel", "### Title\n\nText", nil, PublishOptions{FormattingEnabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestBotPublishesAlbumWithCaption(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 2; i++ {
		path := filepath.Join(dir, string(rune('a'+i))+".jpg")
		if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	requests := 0
	bot := Bot{Token: "test-token", Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if !strings.HasSuffix(req.URL.Path, "/sendMediaGroup") {
			t.Fatalf("unexpected endpoint: %s", req.URL.Path)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if req.FormValue("chat_id") != "@my_channel" {
			t.Fatal("chat ID missing")
		}
		var media []map[string]string
		if err := json.Unmarshal([]byte(req.FormValue("media")), &media); err != nil {
			t.Fatal(err)
		}
		if len(media) != 2 || media[0]["caption"] != "Hello\n\n<a href=\"https://t.me/my_channel\">@my_channel</a>" || media[0]["parse_mode"] != "HTML" || media[1]["caption"] != "" {
			t.Fatalf("unexpected album: %v", media)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":[{"message_id":123},{"message_id":124}]}`)), Header: make(http.Header)}, nil
	})}}
	id, err := bot.Publish(context.Background(), "@my_channel", "Hello", paths, PublishOptions{SignatureText: "@my_channel"})
	if err != nil || id != "123" || requests != 1 {
		t.Fatalf("id=%s requests=%d err=%v", id, requests, err)
	}
}
