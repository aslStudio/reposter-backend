package threads

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const webFixture = `<html><body><script type="application/json" data-sjs>{"require":[{"thread_items":[{"post":{"pk":"101","code":"ABC","caption":{"text":"First post"},"taken_at":1793650000,"user":{"username":"aviasales"},"image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/a.jpg"}]} }},{"post":{"pk":"102","code":"DEF","caption":{"text":"Video post"},"taken_at":1793650100,"user":{"username":"aviasales"},"video_versions":[{"url":"https://scontent.cdninstagram.com/v.mp4"}]}},{"post":{"pk":"103","code":"GHI","caption":{"text":"Reply"},"taken_at":1793650200,"user":{"username":"aviasales"},"text_post_app_info":{"reply_to_author":{"username":"other"}}}},{"post":{"pk":"104","code":"JKL","caption":{"text":"Other account"},"taken_at":1793650300,"user":{"username":"other"}}}]}]}</script></body></html>`

func TestParseWebProfileOwnPostsAndMedia(t *testing.T) {
	posts, err := ParseWebProfile(strings.NewReader(webFixture), "aviasales")
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts=%+v err=%v", posts, err)
	}
	if posts[0].ID != 101 || posts[0].Text != "First post" || posts[0].URL != "https://www.threads.com/@aviasales/post/ABC" || len(posts[0].MediaItems) != 1 || posts[0].MediaItems[0].Kind != "IMAGE" {
		t.Fatalf("image post=%+v", posts[0])
	}
	if posts[1].ID != 102 || len(posts[1].MediaItems) != 1 || posts[1].MediaItems[0].Kind != "VIDEO" {
		t.Fatalf("video post=%+v", posts[1])
	}
}

func TestWebReaderKeepsCursorWhenWindowMissed(t *testing.T) {
	reader := WebReader{Cookie: "sessionid=test", HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "www.threads.com" || req.URL.Path != "/@aviasales" || req.Header.Get("Cookie") != "sessionid=test" {
			t.Errorf("unexpected request %s", req.URL)
		}
		return mockResponse(200, webFixture)
	})}}
	posts, err := reader.ReadSince(context.Background(), "aviasales", 101)
	if err != nil || len(posts) != 1 || posts[0].ID != 102 {
		t.Fatalf("posts=%+v err=%v", posts, err)
	}
	if _, err := reader.ReadSince(context.Background(), "aviasales", 99); err == nil || !strings.Contains(err.Error(), "cursor left unchanged") {
		t.Fatalf("expected backlog error, got %v", err)
	}
}

func TestWebReaderReportsLoginWall(t *testing.T) {
	reader := WebReader{HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return mockResponse(200, `<html><title>Log in to Threads</title></html>`)
	})}}
	if _, err := reader.ReadSince(context.Background(), "aviasales", 0); err == nil || !strings.Contains(err.Error(), "no readable") {
		t.Fatalf("expected unreadable page error, got %v", err)
	}
}
