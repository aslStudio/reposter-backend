package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"reposter/internal/telegram"
)

var handleRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.]{0,29}$`)

func ParseHandle(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "@") {
		raw = raw[1:]
	}
	if handleRE.MatchString(raw) {
		return strings.ToLower(raw), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("expected a public https://www.threads.com/@username link")
	}
	host := strings.ToLower(u.Hostname())
	if host != "threads.com" && host != "www.threads.com" && host != "threads.net" && host != "www.threads.net" {
		return "", errors.New("expected a Threads profile link")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || !strings.HasPrefix(parts[0], "@") || !handleRE.MatchString(parts[0][1:]) {
		return "", errors.New("expected a Threads profile username")
	}
	return strings.ToLower(parts[0][1:]), nil
}

type Client struct {
	AccessToken string
	HTTP        *http.Client
	BaseURL     string
}

func (c Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return "https://graph.threads.net/v1.0"
}

func (c Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	if c.AccessToken == "" {
		return errors.New("THREADS_ACCESS_TOKEN is not configured")
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
				Subcode int    `json:"error_subcode"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		message := problem.Error.Message
		if len(message) > 240 {
			message = message[:240]
		}
		return fmt.Errorf("Threads API HTTP %d code %d subcode %d: %s", resp.StatusCode, problem.Error.Code, problem.Error.Subcode, message)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c Client) VerifyPublisher(ctx context.Context, handle string) error {
	var response struct {
		Username string `json:"username"`
	}
	if err := c.do(ctx, http.MethodGet, "/me?fields=username", nil, &response); err != nil {
		return err
	}
	if !strings.EqualFold(response.Username, handle) {
		return fmt.Errorf("Threads token belongs to @%s, expected @%s", response.Username, handle)
	}
	return nil
}

type mediaItem struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	MediaURL  string `json:"media_url"`
}
type threadPost struct {
	mediaItem
	Text      string `json:"text"`
	Permalink string `json:"permalink"`
	Timestamp string `json:"timestamp"`
	Children  struct {
		Data []mediaItem `json:"data"`
	} `json:"children"`
}

func (c Client) ReadSince(ctx context.Context, handle string, afterID int64) ([]telegram.PublicPost, error) {
	if !handleRE.MatchString(handle) {
		return nil, errors.New("invalid Threads source handle")
	}
	q := url.Values{"username": {handle}, "fields": {"id,text,permalink,timestamp,media_type,media_url,children"}, "limit": {"25"}}
	if afterID == 0 {
		q.Set("limit", "1")
	}
	var out []telegram.PublicPost
	for page := 0; page < 3; page++ {
		var response struct {
			Data   []threadPost `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, "/profile_posts?"+q.Encode(), nil, &response); err != nil {
			return nil, err
		}
		if len(response.Data) == 0 {
			return out, nil
		}
		oldest := int64(0)
		for _, p := range response.Data {
			id, err := strconv.ParseInt(p.ID, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid Threads post ID: %w", err)
			}
			if oldest == 0 || id < oldest {
				oldest = id
			}
			if id <= afterID {
				continue
			}
			post := telegram.PublicPost{ID: id, URL: p.Permalink, Text: p.Text}
			if post.URL == "" {
				return nil, fmt.Errorf("Threads post %s has no permalink", p.ID)
			}
			post.PublishedAt, err = time.Parse(time.RFC3339, p.Timestamp)
			if err != nil {
				post.PublishedAt, err = time.Parse("2006-01-02T15:04:05-0700", p.Timestamp)
			}
			if err != nil {
				return nil, fmt.Errorf("Threads post %s has invalid timestamp: %w", p.ID, err)
			}
			items := []mediaItem{p.mediaItem}
			if p.MediaType == "CAROUSEL_ALBUM" {
				items = p.Children.Data
				if len(items) == 0 {
					return nil, fmt.Errorf("Threads carousel post %s has no children", p.ID)
				}
			}
			for _, item := range items {
				if item.ID != "" && (item.MediaType == "" || ((item.MediaType == "IMAGE" || item.MediaType == "VIDEO") && item.MediaURL == "")) {
					if err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(item.ID)+"?fields=id,media_type,media_url", nil, &item); err != nil {
						return nil, err
					}
				}
				if item.MediaType == "IMAGE" || item.MediaType == "VIDEO" {
					if item.MediaURL == "" {
						return nil, fmt.Errorf("Threads post %s has media without a URL", p.ID)
					}
					post.MediaItems = append(post.MediaItems, telegram.MediaItem{URL: item.MediaURL, Kind: item.MediaType})
					if item.MediaType == "IMAGE" {
						post.ImageURLs = append(post.ImageURLs, item.MediaURL)
					} else {
						post.VideoURLs = append(post.VideoURLs, item.MediaURL)
					}
				}
			}
			out = append(out, post)
		}
		if afterID == 0 || oldest <= afterID || response.Paging.Cursors.After == "" {
			return out, nil
		}
		q.Set("after", response.Paging.Cursors.After)
	}
	return nil, errors.New("Threads backlog exceeds three pages; cursor left unchanged")
}

func (c Client) Publish(ctx context.Context, handle, text string, mediaURLs, mediaPaths []string) (string, error) {
	text = strings.TrimSpace(text)
	if len(mediaURLs) != len(mediaPaths) {
		return "", errors.New("Threads media URL and file counts differ")
	}
	if (text == "" && len(mediaURLs) == 0) || utf8.RuneCountInString(text) > 500 {
		return "", errors.New("Threads post needs text or media and supports at most 500 characters")
	}
	if len(mediaURLs) > 20 {
		return "", errors.New("Threads carousel exceeds 20 media items")
	}
	if err := c.VerifyPublisher(ctx, handle); err != nil {
		return "", err
	}
	create := func(values url.Values) (string, error) {
		var response struct {
			ID string `json:"id"`
		}
		if err := c.do(ctx, http.MethodPost, "/me/threads", values, &response); err != nil {
			return "", err
		}
		if response.ID == "" {
			return "", errors.New("Threads media container returned no ID")
		}
		return response.ID, nil
	}
	var containerID string
	var err error
	if len(mediaURLs) == 0 {
		containerID, err = create(url.Values{"media_type": {"TEXT"}, "text": {text}})
	} else if len(mediaURLs) == 1 {
		values, e := mediaForm(mediaURLs[0], mediaPaths[0])
		if e != nil {
			return "", e
		}
		if text != "" {
			values.Set("text", text)
		}
		containerID, err = create(values)
	} else {
		ids := make([]string, 0, len(mediaURLs))
		for i, raw := range mediaURLs {
			values, e := mediaForm(raw, mediaPaths[i])
			if e != nil {
				return "", e
			}
			values.Set("is_carousel_item", "true")
			id, e := create(values)
			if e != nil {
				return "", e
			}
			if e = c.waitContainer(ctx, id); e != nil {
				return "", e
			}
			ids = append(ids, id)
		}
		containerID, err = create(url.Values{"media_type": {"CAROUSEL"}, "children": {strings.Join(ids, ",")}, "text": {text}})
	}
	if err != nil {
		return "", err
	}
	if err := c.waitContainer(ctx, containerID); err != nil {
		return "", err
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/me/threads_publish", url.Values{"creation_id": {containerID}}, &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", errors.New("Threads publish returned no post ID")
	}
	return response.ID, nil
}

func mediaForm(raw, path string) (url.Values, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, errors.New("Threads media must have a public HTTPS URL")
	}
	if strings.EqualFold(filepath.Ext(path), ".mp4") {
		return url.Values{"media_type": {"VIDEO"}, "video_url": {raw}}, nil
	}
	return url.Values{"media_type": {"IMAGE"}, "image_url": {raw}}, nil
}

func (c Client) waitContainer(ctx context.Context, id string) error {
	for attempt := 0; attempt < 10; attempt++ {
		var status struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"error_message"`
		}
		if err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(id)+"?fields=status,error_message", nil, &status); err != nil {
			return err
		}
		switch status.Status {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			return fmt.Errorf("Threads container %s: %s", status.Status, status.ErrorMessage)
		case "PUBLISHED":
			return errors.New("Threads container is already published; inspect before retry")
		case "IN_PROGRESS":
		default:
			return fmt.Errorf("unexpected Threads container status %q", status.Status)
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("Threads container did not finish processing within five minutes")
}
