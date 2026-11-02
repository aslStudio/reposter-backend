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

func TestBotPublishesVideo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	bot := Bot{Token: "test-token", Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendVideo") {
			t.Errorf("endpoint %s", req.URL.Path)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if req.FormValue("caption") != "Video" || req.FormValue("supports_streaming") != "true" {
			t.Errorf("form %+v", req.Form)
		}
		if _, _, err := req.FormFile("video"); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":45}}`)), Header: make(http.Header)}, nil
	})}}
	id, err := bot.Publish(context.Background(), "@my_channel", "Video", []string{path}, PublishOptions{})
	if err != nil || id != "45" {
		t.Fatalf("id=%s err=%v", id, err)
	}
}

func TestBotPublishesMixedAlbum(t *testing.T) {
	dir := t.TempDir()
	photo := filepath.Join(dir, "photo.jpg")
	video := filepath.Join(dir, "clip.mp4")
	for _, path := range []string{photo, video} {
		if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bot := Bot{Token: "test-token", Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendMediaGroup") {
			t.Errorf("endpoint %s", req.URL.Path)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		var media []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(req.FormValue("media")), &media); err != nil {
			t.Fatal(err)
		}
		if len(media) != 2 || media[0].Type != "photo" || media[1].Type != "video" {
			t.Errorf("media %+v", media)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":[{"message_id":45},{"message_id":46}]}`)), Header: make(http.Header)}, nil
	})}}
	if _, err := bot.Publish(context.Background(), "@my_channel", "", []string{photo, video}, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
}
