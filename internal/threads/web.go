package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"reposter/internal/telegram"
)

// WebReader reads the visible profile page using a separately configured
// browser session. It deliberately does not use the publishing API token.
type WebReader struct {
	Cookie string
	HTTP   *http.Client
}

func (r WebReader) ReadSince(ctx context.Context, handle string, afterID int64) ([]telegram.PublicPost, error) {
	if !handleRE.MatchString(handle) {
		return nil, errors.New("invalid Threads source handle")
	}
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || req.URL.Hostname() != "www.threads.com" || len(via) > 3 {
				return errors.New("Threads profile redirected outside threads.com")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.threads.com/@"+handle, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Reposter/0.1)")
	if r.Cookie != "" {
		req.Header.Set("Cookie", r.Cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request != nil && strings.HasPrefix(resp.Request.URL.Path, "/login") {
		return nil, errors.New("Threads web source requires a valid THREADS_SOURCE_COOKIE session")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Threads web source HTTP %d", resp.StatusCode)
	}
	posts, err := ParseWebProfile(io.LimitReader(resp.Body, 8<<20), handle)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, errors.New("Threads profile page contains no readable posts")
	}
	oldest := posts[0].ID
	var out []telegram.PublicPost
	for _, p := range posts {
		if p.ID < oldest {
			oldest = p.ID
		}
		if p.ID > afterID {
			out = append(out, p)
		}
	}
	if afterID > 0 && oldest > afterID {
		return nil, errors.New("Threads posts since cursor exceed the visible page; cursor left unchanged")
	}
	return out, nil
}

func ParseWebProfile(input io.Reader, handle string) ([]telegram.PublicPost, error) {
	root, err := html.Parse(input)
	if err != nil {
		return nil, err
	}
	var posts []telegram.PublicPost
	seen := map[int64]bool{}
	var walkScripts func(*html.Node)
	walkScripts = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" && n.FirstChild != nil {
			isJSON := false
			for _, a := range n.Attr {
				if a.Key == "type" && a.Val == "application/json" {
					isJSON = true
				}
			}
			if isJSON && strings.Contains(n.FirstChild.Data, "thread_items") {
				var data any
				dec := json.NewDecoder(strings.NewReader(n.FirstChild.Data))
				dec.UseNumber()
				if dec.Decode(&data) == nil {
					collectWebPosts(data, handle, seen, &posts, 0)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkScripts(c)
		}
	}
	walkScripts(root)
	if len(posts) == 0 {
		return nil, errors.New("Threads page has no readable post data")
	}
	return posts, nil
}

func collectWebPosts(data any, handle string, seen map[int64]bool, out *[]telegram.PublicPost, depth int) {
	if depth > 40 {
		return
	}
	switch v := data.(type) {
	case map[string]any:
		if items, ok := v["thread_items"].([]any); ok {
			for _, item := range items {
				entry, _ := item.(map[string]any)
				post, _ := entry["post"].(map[string]any)
				if p, ok := parseWebPost(post, handle); ok && !seen[p.ID] {
					seen[p.ID] = true
					*out = append(*out, p)
				}
			}
		}
		for _, child := range v {
			collectWebPosts(child, handle, seen, out, depth+1)
		}
	case []any:
		for _, child := range v {
			collectWebPosts(child, handle, seen, out, depth+1)
		}
	}
}

func parseWebPost(data map[string]any, handle string) (telegram.PublicPost, bool) {
	if data == nil {
		return telegram.PublicPost{}, false
	}
	user, _ := data["user"].(map[string]any)
	if !strings.EqualFold(webString(user["username"]), handle) {
		return telegram.PublicPost{}, false
	}
	info, _ := data["text_post_app_info"].(map[string]any)
	if info["reply_to_author"] != nil {
		return telegram.PublicPost{}, false
	}
	id, err := strconv.ParseInt(webString(data["pk"]), 10, 64)
	code := webString(data["code"])
	if err != nil || id <= 0 || code == "" {
		return telegram.PublicPost{}, false
	}
	post := telegram.PublicPost{ID: id, URL: "https://www.threads.com/@" + handle + "/post/" + url.PathEscape(code)}
	caption, _ := data["caption"].(map[string]any)
	post.Text = webString(caption["text"])
	if raw := webString(data["taken_at"]); raw != "" {
		seconds, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			post.PublishedAt = time.Unix(seconds, 0).UTC()
		}
	}
	if post.PublishedAt.IsZero() {
		return telegram.PublicPost{}, false
	}
	addMedia := func(media map[string]any) {
		if media == nil {
			return
		}
		if video := firstWebMediaURL(media, "video_versions"); video != "" {
			post.MediaItems = append(post.MediaItems, telegram.MediaItem{URL: video, Kind: "VIDEO"})
			post.VideoURLs = append(post.VideoURLs, video)
		} else if image := firstWebMediaURL(media, "image_versions2"); image != "" {
			post.MediaItems = append(post.MediaItems, telegram.MediaItem{URL: image, Kind: "IMAGE"})
			post.ImageURLs = append(post.ImageURLs, image)
		}
	}
	if items, ok := data["carousel_media"].([]any); ok && len(items) > 0 {
		for _, item := range items {
			media, _ := item.(map[string]any)
			addMedia(media)
		}
	} else {
		addMedia(data)
	}
	return post, true
}

func firstWebMediaURL(media map[string]any, key string) string {
	var entries []any
	if key == "image_versions2" {
		versions, _ := media[key].(map[string]any)
		entries, _ = versions["candidates"].([]any)
	} else {
		entries, _ = media[key].([]any)
	}
	for _, item := range entries {
		entry, _ := item.(map[string]any)
		if raw := webString(entry["url"]); raw != "" {
			return raw
		}
	}
	return ""
}

func webString(value any) string {
	switch x := value.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}
