package x

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSinceAndPhotos(t *testing.T) {
	var calls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("missing bearer auth")
		}
		if r.URL.Path == "/2/users/by/username/foxnews" {
			_, _ = w.Write([]byte(`{"data":{"id":"123"}}`))
			return
		}
		if r.URL.Path != "/2/users/123/tweets" || r.URL.Query().Get("since_id") != "100" || r.URL.Query().Get("post.fields") != "created_at,attachments" {
			t.Errorf("unexpected timeline request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"102","text":"new story","attachments":{"media_keys":["photo","video"]}}],"includes":{"media":[{"media_key":"photo","type":"photo","url":"https://pbs.twimg.com/a.jpg"},{"media_key":"video","type":"video","variants":[{"content_type":"application/x-mpegURL","url":"https://video.twimg.com/a.m3u8"},{"content_type":"video/mp4","bit_rate":832000,"url":"https://video.twimg.com/a.mp4"}]}]}}`))
	})
	c := Client{BearerToken: "test", BaseURL: "https://api.x.com", HTTP: mockClient(handler)}
	id, err := c.ResolveUser(context.Background(), "foxnews")
	if err != nil || id != "123" {
		t.Fatalf("resolve %q: %v", id, err)
	}
	posts, err := c.ReadSince(context.Background(), "foxnews", id, 100)
	if err != nil || len(posts) != 1 || posts[0].ID != 102 || len(posts[0].ImageURLs) != 1 || posts[0].ImageURLs[0] != "https://pbs.twimg.com/a.jpg" || len(posts[0].VideoURLs) != 1 || posts[0].VideoURLs[0] != "https://video.twimg.com/a.mp4" {
		t.Fatalf("posts %+v: %v", posts, err)
	}
	if calls != 2 {
		t.Fatalf("got %d requests", calls)
	}
}
func TestPublishAndAccountCheck(t *testing.T) {
	calls := []string{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), `OAuth oauth_consumer_key="`) || !strings.Contains(r.Header.Get("Authorization"), `oauth_signature="`) {
			t.Errorf("invalid OAuth header: %q", r.Header.Get("Authorization"))
		}
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/2/users/me":
			_, _ = w.Write([]byte(`{"data":{"username":"aslDigitalVlad"}}`))
		case "/2/media/upload":
			var p struct {
				Media    string `json:"media"`
				Category string `json:"media_category"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			data, err := base64.StdEncoding.DecodeString(p.Media)
			if err != nil || len(data) == 0 || p.Category != "tweet_image" {
				t.Errorf("invalid image payload")
			}
			_, _ = w.Write([]byte(`{"data":{"id":"media1"}}`))
		case "/2/tweets":
			var p struct {
				Text  string `json:"text"`
				Media struct {
					IDs []string `json:"media_ids"`
				} `json:"media"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			if p.Text != "Headline\nNews" || len(p.Media.IDs) != 1 || p.Media.IDs[0] != "media1" {
				t.Errorf("unexpected post payload: %+v", p)
			}
			_, _ = w.Write([]byte(`{"data":{"id":"post1"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	c := Client{BaseURL: "https://api.x.com", HTTP: mockClient(handler), ConsumerKey: "ck", ConsumerSecret: "cs", AccessToken: "at", AccessSecret: "as"}
	if err := c.VerifyPublisher(context.Background(), "aslDigitalVlad"); err != nil {
		t.Fatal(err)
	}
	if err := c.VerifyPublisher(context.Background(), "other"); err == nil {
		t.Fatal("wrong account accepted")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0, 0, 0, 0}, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := c.Publish(context.Background(), "Headline\nNews", []string{path})
	if err != nil || id != "post1" {
		t.Fatalf("publish %q: %v", id, err)
	}
	if len(calls) != 4 {
		t.Fatalf("got %v calls", calls)
	}
}
func TestParseHandle(t *testing.T) {
	for _, raw := range []string{"@FoxNews", "https://x.com/FoxNews", "https://twitter.com/FoxNews/"} {
		h, err := ParseHandle(raw)
		if err != nil || h != "foxnews" {
			t.Errorf("%q -> %q, %v", raw, h, err)
		}
	}
	for _, raw := range []string{"https://evil.example/FoxNews", "https://x.com/FoxNews/status/1", "https://x.com/FoxNews?x=1", "https://x.com@evil.example/FoxNews"} {
		if _, err := ParseHandle(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

// mockClient runs the real request builder without opening a socket.
type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockClient(h http.Handler) *http.Client {
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})}
}

func TestPINAuthorization(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "OAuth ") || !strings.Contains(header, `oauth_signature="`) {
			t.Errorf("unsigned OAuth request: %q", header)
		}
		switch r.URL.Path {
		case "/oauth/request_token":
			if !strings.Contains(header, `oauth_callback="oob"`) || strings.Contains(header, `oauth_token="`) {
				t.Error("invalid request-token auth")
			}
			_, _ = w.Write([]byte("oauth_token=temp&oauth_token_secret=temporary-secret&oauth_callback_confirmed=true"))
		case "/oauth/access_token":
			if !strings.Contains(header, `oauth_token="temp"`) || !strings.Contains(header, `oauth_verifier="1234567"`) {
				t.Error("invalid access-token auth")
			}
			_, _ = w.Write([]byte("oauth_token=user-token&oauth_token_secret=user-secret"))
		default:
			t.Errorf("unexpected OAuth path %s", r.URL.Path)
		}
	})
	c := Client{BaseURL: "https://api.x.com", HTTP: mockClient(handler), ConsumerKey: "app-key", ConsumerSecret: "app-secret"}
	token, secret, authURL, err := c.BeginPIN(context.Background())
	if err != nil || token != "temp" || secret != "temporary-secret" || authURL != "https://api.x.com/oauth/authorize?oauth_token=temp" {
		t.Fatalf("begin: %q %q %q %v", token, secret, authURL, err)
	}
	userToken, userSecret, err := c.CompletePIN(context.Background(), token, secret, "1234567")
	if err != nil || userToken != "user-token" || userSecret != "user-secret" {
		t.Fatalf("complete: %q %q %v", userToken, userSecret, err)
	}
	if calls != 2 {
		t.Fatalf("got %d calls", calls)
	}
}

func TestTokenCipher(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := NewTokenCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal("secret")
	if err != nil || string(sealed) == "secret" {
		t.Fatalf("seal: %v", err)
	}
	opened, err := cipher.Open(sealed)
	if err != nil || opened != "secret" {
		t.Fatalf("open: %q %v", opened, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = cipher.Open(sealed); err == nil {
		t.Fatal("tampered token accepted")
	}
}
