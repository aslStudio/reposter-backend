package x

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishVideoCompletesUploadBeforePost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, append([]byte("0000ftypisom"), []byte("video data")...), 0600); err != nil {
		t.Fatal(err)
	}
	steps := []string{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "OAuth ") {
			t.Error("unsigned request")
		}
		steps = append(steps, r.URL.Path)
		switch r.URL.Path {
		case "/2/media/upload/initialize":
			var p struct {
				Type string `json:"media_type"`
				Size int64  `json:"total_bytes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Type != "video/mp4" || p.Size != 22 {
				t.Errorf("initialize: %+v, %v", p, err)
			}
			_, _ = w.Write([]byte(`{"data":{"id":"vid1"}}`))
		case "/2/media/upload/vid1/append":
			var p struct {
				Media string `json:"media"`
				Index int    `json:"segment_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Fatal(err)
			}
			data, err := base64.StdEncoding.DecodeString(p.Media)
			if err != nil || p.Index != 0 || len(data) != 22 {
				t.Errorf("append: %+v, %v", p, err)
			}
			_, _ = w.Write([]byte(`{}`))
		case "/2/media/upload/vid1/finalize":
			_, _ = w.Write([]byte(`{"data":{"id":"vid1","processing_info":{"state":"succeeded"}}}`))
		case "/2/tweets":
			var p struct {
				Media struct {
					IDs []string `json:"media_ids"`
				} `json:"media"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil || len(p.Media.IDs) != 1 || p.Media.IDs[0] != "vid1" {
				t.Errorf("tweet: %+v, %v", p, err)
			}
			_, _ = w.Write([]byte(`{"data":{"id":"post1"}}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	c := Client{BaseURL: "https://api.x.com", HTTP: mockClient(handler), ConsumerKey: "ck", ConsumerSecret: "cs", AccessToken: "at", AccessSecret: "as"}
	id, err := c.Publish(context.Background(), "", []string{path})
	if err != nil || id != "post1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	want := []string{"/2/media/upload/initialize", "/2/media/upload/vid1/append", "/2/media/upload/vid1/finalize", "/2/tweets"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps %v", steps)
	}
}
