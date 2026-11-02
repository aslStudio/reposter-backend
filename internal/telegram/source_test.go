package telegram

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestParsePublicPage(t *testing.T) {
	page := `<html><body>
<div class="tgme_widget_message" data-post="example_channel/41">
  <div class="tgme_widget_message_text">First &amp; second<br>line</div>
  <a class="tgme_widget_message_photo_wrap" style="background-image:url('https://cdn4.telesco.pe/file/photo.jpg')"></a>
  <time datetime="2026-11-02T12:30:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="other_channel/99"><div class="tgme_widget_message_text">Ignore</div></div>
</body></html>`
	posts, err := ParsePublicPage(strings.NewReader(page), "example_channel")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts", len(posts))
	}
	p := posts[0]
	if p.ID != 41 || p.URL != "https://t.me/example_channel/41" || p.Text != "First & second\nline" {
		t.Fatalf("unexpected post: %+v", p)
	}
	if len(p.ImageURLs) != 1 || p.ImageURLs[0] != "https://cdn4.telesco.pe/file/photo.jpg" {
		t.Fatalf("unexpected images: %v", p.ImageURLs)
	}
	if p.PublishedAt.IsZero() {
		t.Fatal("publication time not parsed")
	}
}

func TestParsePublicPageRetainsChannelLinkForFooterCleanup(t *testing.T) {
	page := `<div class="tgme_widget_message" data-post="example_channel/42"><div class="tgme_widget_message_text">Новость<br><br><a href="https://t.me/other_channel">Наш канал</a></div></div>`
	posts, err := ParsePublicPage(strings.NewReader(page), "example_channel")
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%d err=%v", len(posts), err)
	}
	if posts[0].Text != "Новость\n\nНаш канал (https://t.me/other_channel)" {
		t.Fatalf("unexpected post text: %q", posts[0].Text)
	}
}

func TestParsePublicPageVideo(t *testing.T) {
	page := `<div class="tgme_widget_message" data-post="example_channel/43"><div class="tgme_widget_message_video_player"><video class="tgme_widget_message_video" src="https://cdn4.telesco.pe/file/clip.mp4"></video></div></div>`
	posts, err := ParsePublicPage(strings.NewReader(page), "example_channel")
	if err != nil || len(posts) != 1 || len(posts[0].VideoURLs) != 1 || posts[0].VideoURLs[0] != "https://cdn4.telesco.pe/file/clip.mp4" {
		t.Fatalf("posts=%+v err=%v", posts, err)
	}
}

func TestReaderPaginatesToCursor(t *testing.T) {
	requests := 0
	reader := Reader{Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		var page string
		if req.URL.Query().Get("before") == "" {
			page = `<div class="tgme_widget_message" data-post="sample_channel/11"><div class="tgme_widget_message_text">Eleven</div></div><div class="tgme_widget_message" data-post="sample_channel/12"><div class="tgme_widget_message_text">Twelve</div></div>`
		} else if req.URL.Query().Get("before") == "11" {
			page = `<div class="tgme_widget_message" data-post="sample_channel/9"><div class="tgme_widget_message_text">Nine</div></div><div class="tgme_widget_message" data-post="sample_channel/10"><div class="tgme_widget_message_text">Ten</div></div>`
		} else {
			t.Fatalf("unexpected before cursor %q", req.URL.Query().Get("before"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(page)), Header: make(http.Header)}, nil
	})}}
	posts, err := reader.ReadSince(context.Background(), "sample_channel", 9)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(posts) != 3 {
		t.Fatalf("requests=%d posts=%d", requests, len(posts))
	}
}

func TestCapturedPublicPage(t *testing.T) {
	path := os.Getenv("TELEGRAM_HTML_FIXTURE")
	if path == "" {
		t.Skip("set TELEGRAM_HTML_FIXTURE to a captured public channel page")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	posts, err := ParsePublicPage(f, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) == 0 {
		t.Fatal("no public posts found in captured Telegram HTML")
	}
}

func TestParseHandle(t *testing.T) {
	for _, raw := range []string{"@Example_Channel", "https://t.me/Example_Channel", "https://t.me/s/Example_Channel"} {
		handle, err := ParseHandle(raw)
		if err != nil || handle != "example_channel" {
			t.Fatalf("%q: handle=%q err=%v", raw, handle, err)
		}
	}
	for _, raw := range []string{"https://example.org/channel", "https://t.me/+privateinvite", "https://t.me/c/123/4"} {
		if _, err := ParseHandle(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
