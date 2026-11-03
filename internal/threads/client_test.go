package threads

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func mockResponse(code int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func TestParseHandle(t *testing.T) {
	for _, raw := range []string{"@Aviasales", "https://www.threads.com/@Aviasales", "https://threads.net/@Aviasales/"} {
		got, err := ParseHandle(raw)
		if err != nil || got != "aviasales" {
			t.Errorf("%q -> %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"https://evil.example/@aviasales", "https://www.threads.com/@aviasales/post/1", "https://www.threads.com/@aviasales?q=1"} {
		if _, err := ParseHandle(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestReadSinceFirstPollReturnsOnlyLatest(t *testing.T) {
	called := 0
	client := Client{AccessToken: "secret", HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		called++
		if req.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing bearer token")
		}
		if req.URL.Path != "/v1.0/profile_posts" || req.URL.Query().Get("username") != "aviasales" || req.URL.Query().Get("limit") != "1" {
			t.Errorf("unexpected request: %s", req.URL.String())
		}
		return mockResponse(200, `{"data":[{"id":"101","text":"Latest story","permalink":"https://www.threads.com/@aviasales/post/abc","timestamp":"2026-11-02T20:00:00+0000","media_type":"TEXT_POST"}]}`)
	})}}
	posts, err := client.ReadSince(context.Background(), "aviasales", 0)
	if err != nil || called != 1 || len(posts) != 1 || posts[0].ID != 101 || posts[0].Text != "Latest story" {
		t.Fatalf("calls=%d posts=%+v err=%v", called, posts, err)
	}
}

func TestReadSinceRetainsCarouselOrder(t *testing.T) {
	client := Client{AccessToken: "secret", HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1.0/profile_posts":
			return mockResponse(200, `{"data":[{"id":"102","text":"Trip","permalink":"https://www.threads.com/@aviasales/post/abc","timestamp":"2026-11-02T20:00:00+0000","media_type":"CAROUSEL_ALBUM","children":{"data":[{"id":"200","media_type":"VIDEO","media_url":"https://scontent.xx.fbcdn.net/video.mp4"},{"id":"201","media_type":"IMAGE","media_url":"https://scontent.xx.fbcdn.net/photo.jpg"}]}}]}`)
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			return mockResponse(404, `{}`)
		}
	})}}
	posts, err := client.ReadSince(context.Background(), "aviasales", 0)
	if err != nil || len(posts) != 1 || len(posts[0].MediaItems) != 2 || posts[0].MediaItems[0].Kind != "VIDEO" || posts[0].MediaItems[1].Kind != "IMAGE" {
		t.Fatalf("posts=%+v err=%v", posts, err)
	}
}

func TestPublishWaitsForContainerAndVerifiesAccount(t *testing.T) {
	steps := []string{}
	client := Client{AccessToken: "secret", HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		steps = append(steps, req.URL.Path)
		if req.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing bearer token")
		}
		switch req.URL.Path {
		case "/v1.0/me":
			return mockResponse(200, `{"username":"vastahovv"}`)
		case "/v1.0/me/threads":
			data, _ := io.ReadAll(req.Body)
			values, _ := url.ParseQuery(string(data))
			if values.Get("media_type") != "TEXT" || values.Get("text") != "Headline" {
				t.Errorf("container form %v", values)
			}
			return mockResponse(200, `{"id":"container1"}`)
		case "/v1.0/container1":
			return mockResponse(200, `{"status":"FINISHED"}`)
		case "/v1.0/me/threads_publish":
			return mockResponse(200, `{"id":"post1"}`)
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			return mockResponse(404, `{}`)
		}
	})}}
	id, err := client.Publish(context.Background(), "vastahovv", "Headline", nil, nil)
	if err != nil || id != "post1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if got := strings.Join(steps, ","); got != "/v1.0/me,/v1.0/me/threads,/v1.0/container1,/v1.0/me/threads_publish" {
		t.Fatalf("steps %s", got)
	}
	if err := client.VerifyPublisher(context.Background(), "wrong"); err == nil {
		t.Fatal("accepted wrong destination")
	}
}

func TestReadSinceReportsProfilePermissionError(t *testing.T) {
	client := Client{AccessToken: "secret", HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return mockResponse(403, `{"error":{"message":"Permission not approved for this profile","code":10}}`)
	})}}
	if _, err := client.ReadSince(context.Background(), "aviasales", 0); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error %v", err)
	}
}

func TestPublishVideoWaitsAndStopsOnProcessingError(t *testing.T) {
	published := false
	client := Client{AccessToken: "secret", HTTP: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1.0/me":
			return mockResponse(200, `{"username":"vastahovv"}`)
		case "/v1.0/me/threads":
			data, _ := io.ReadAll(req.Body)
			values, _ := url.ParseQuery(string(data))
			if values.Get("media_type") != "VIDEO" || values.Get("video_url") != "https://scontent.xx.fbcdn.net/video?id=1" {
				t.Errorf("form %v", values)
			}
			return mockResponse(200, `{"id":"container1"}`)
		case "/v1.0/container1":
			return mockResponse(200, `{"status":"ERROR","error_message":"FAILED_DOWNLOADING_VIDEO"}`)
		case "/v1.0/me/threads_publish":
			published = true
			return mockResponse(200, `{"id":"post1"}`)
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			return mockResponse(404, `{}`)
		}
	})}}
	_, err := client.Publish(context.Background(), "vastahovv", "Video", []string{"https://scontent.xx.fbcdn.net/video?id=1"}, []string{"/data/video.mp4"})
	if err == nil || !strings.Contains(err.Error(), "FAILED_DOWNLOADING_VIDEO") || published {
		t.Fatalf("err=%v published=%t", err, published)
	}
}
