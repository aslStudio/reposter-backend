package x

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"reposter/internal/telegram"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

func ParseHandle(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "@") {
		raw = raw[1:]
	}
	if usernameRE.MatchString(raw) {
		return strings.ToLower(raw), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Hostname() != "x.com" && u.Hostname() != "twitter.com") || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("expected public https://x.com/username link")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || !usernameRE.MatchString(parts[0]) {
		return "", errors.New("expected X profile username")
	}
	return strings.ToLower(parts[0]), nil
}

type Client struct {
	BearerToken    string
	ConsumerKey    string
	ConsumerSecret string
	AccessToken    string
	AccessSecret   string
	HTTP           *http.Client
	BaseURL        string
}

func (c Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return "https://api.x.com"
}
func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}
func (c Client) CanPublish() bool {
	return c.ConsumerKey != "" && c.ConsumerSecret != "" && c.AccessToken != "" && c.AccessSecret != ""
}
func IsOAuthPermissionError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "X API HTTP 403: Your client app is not configured with the appropriate oauth1 app permissions for this endpoint.")
}

func (c Client) get(ctx context.Context, path string, result any) error {
	if c.BearerToken == "" {
		return errors.New("X_BEARER_TOKEN is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	return c.do(req, result)
}
func (c Client) do(req *http.Request, result any) error {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
			Errors []struct {
				Message string `json:"message"`
				Detail  string `json:"detail"`
				Title   string `json:"title"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(data, &problem)
		detail := problem.Detail
		if detail == "" {
			detail = problem.Title
		}
		if detail == "" && len(problem.Errors) > 0 {
			detail = problem.Errors[0].Message
			if detail == "" {
				detail = problem.Errors[0].Detail
			}
			if detail == "" {
				detail = problem.Errors[0].Title
			}
		}
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return fmt.Errorf("X API HTTP %d: %s", resp.StatusCode, detail)
	}
	if result != nil {
		return json.Unmarshal(data, result)
	}
	return nil
}
func (c Client) ResolveUser(ctx context.Context, handle string) (string, error) {
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/2/users/by/username/"+url.PathEscape(handle), &response); err != nil {
		return "", err
	}
	if response.Data.ID == "" {
		return "", errors.New("X user lookup returned no ID")
	}
	return response.Data.ID, nil
}

// ReadSince fetches at most three pages. If the backlog is larger, the cursor stays
// unchanged so a later operator action can handle it without silently losing posts.
func (c Client) ReadSince(ctx context.Context, handle, userID string, afterID int64) ([]telegram.PublicPost, error) {
	if !usernameRE.MatchString(handle) || userID == "" {
		return nil, errors.New("invalid X source")
	}
	var out []telegram.PublicPost
	token := ""
	for page := 0; page < 3; page++ {
		q := url.Values{"max_results": {"10"}, "exclude": {"replies,retweets"}, "post.fields": {"created_at,attachments"}, "expansions": {"attachments.media_keys"}, "media.fields": {"type,url,variants"}}
		if afterID > 0 {
			q.Set("since_id", strconv.FormatInt(afterID, 10))
		}
		if token != "" {
			q.Set("pagination_token", token)
		}
		var response struct {
			Data []struct {
				ID          string    `json:"id"`
				Text        string    `json:"text"`
				CreatedAt   time.Time `json:"created_at"`
				Attachments struct {
					MediaKeys []string `json:"media_keys"`
				} `json:"attachments"`
			} `json:"data"`
			Includes struct {
				Media []struct {
					Key      string `json:"media_key"`
					Type     string `json:"type"`
					URL      string `json:"url"`
					Variants []struct {
						BitRate     int    `json:"bit_rate"`
						ContentType string `json:"content_type"`
						URL         string `json:"url"`
					} `json:"variants"`
				} `json:"media"`
			} `json:"includes"`
			Meta struct {
				NextToken string `json:"next_token"`
			} `json:"meta"`
		}
		if err := c.get(ctx, "/2/users/"+url.PathEscape(userID)+"/tweets?"+q.Encode(), &response); err != nil {
			return nil, err
		}
		photos := make(map[string]string)
		videos := make(map[string]string)
		for _, m := range response.Includes.Media {
			if m.Type == "photo" && m.URL != "" {
				photos[m.Key] = m.URL
			}
			if m.Type == "video" || m.Type == "animated_gif" {
				bestRate := -1
				for _, variant := range m.Variants {
					if variant.ContentType == "video/mp4" && variant.URL != "" && variant.BitRate >= bestRate {
						bestRate, videos[m.Key] = variant.BitRate, variant.URL
					}
				}
				if videos[m.Key] == "" {
					return nil, fmt.Errorf("X video %s has no downloadable MP4 variant", m.Key)
				}
			}
		}
		for _, p := range response.Data {
			id, err := strconv.ParseInt(p.ID, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid X post ID: %w", err)
			}
			post := telegram.PublicPost{ID: id, URL: "https://x.com/" + handle + "/status/" + p.ID, Text: p.Text, PublishedAt: p.CreatedAt}
			for _, key := range p.Attachments.MediaKeys {
				if photo := photos[key]; photo != "" {
					post.ImageURLs = append(post.ImageURLs, photo)
				}
				if video := videos[key]; video != "" {
					post.VideoURLs = append(post.VideoURLs, video)
				}
			}
			out = append(out, post)
		}
		token = response.Meta.NextToken
		if afterID == 0 || token == "" {
			return out, nil
		}
	}
	return nil, errors.New("X backlog exceeds three pages; cursor left unchanged")
}

func oauthEncode(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func (c Client) sign(req *http.Request) error {
	if !c.CanPublish() {
		return errors.New("X user access token and secret are required for publication")
	}
	return c.signWith(req, c.AccessToken, c.AccessSecret, nil)
}
func (c Client) signWith(req *http.Request, token, secret string, extra map[string]string) error {
	if c.ConsumerKey == "" || c.ConsumerSecret == "" {
		return errors.New("X app key and secret are required")
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	params := map[string]string{"oauth_consumer_key": c.ConsumerKey, "oauth_nonce": hex.EncodeToString(nonceBytes), "oauth_signature_method": "HMAC-SHA1", "oauth_timestamp": strconv.FormatInt(time.Now().Unix(), 10), "oauth_version": "1.0"}
	if token != "" {
		params["oauth_token"] = token
	}
	for k, v := range extra {
		params[k] = v
	}
	pairs := make([]string, 0, len(params)+len(req.URL.Query()))
	for key, val := range params {
		pairs = append(pairs, oauthEncode(key)+"="+oauthEncode(val))
	}
	for key, vals := range req.URL.Query() {
		for _, val := range vals {
			pairs = append(pairs, oauthEncode(key)+"="+oauthEncode(val))
		}
	}
	sortStrings(pairs)
	baseURL := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
	signatureBase := req.Method + "&" + oauthEncode(baseURL) + "&" + oauthEncode(strings.Join(pairs, "&"))
	key := oauthEncode(c.ConsumerSecret) + "&" + oauthEncode(secret)
	mac := hmac.New(sha1.New, []byte(key))
	_, _ = mac.Write([]byte(signatureBase))
	params["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	header := make([]string, 0, len(params))
	for k, v := range params {
		header = append(header, oauthEncode(k)+`="`+oauthEncode(v)+`"`)
	}
	sortStrings(header)
	req.Header.Set("Authorization", "OAuth "+strings.Join(header, ", "))
	return nil
}
func (c Client) oauthCall(ctx context.Context, path, token, secret string, extra map[string]string) (url.Values, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, nil)
	if err != nil {
		return nil, err
	}
	if err = c.signWith(req, token, secret, extra); err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("X OAuth HTTP %d", resp.StatusCode)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, errors.New("invalid X OAuth response")
	}
	return values, nil
}
func (c Client) BeginPIN(ctx context.Context) (string, string, string, error) {
	values, err := c.oauthCall(ctx, "/oauth/request_token", "", "", map[string]string{"oauth_callback": "oob"})
	if err != nil {
		return "", "", "", err
	}
	token, secret := values.Get("oauth_token"), values.Get("oauth_token_secret")
	if token == "" || secret == "" || values.Get("oauth_callback_confirmed") != "true" {
		return "", "", "", errors.New("X did not confirm PIN authorization")
	}
	return token, secret, c.base() + "/oauth/authorize?oauth_token=" + url.QueryEscape(token), nil
}
func (c Client) CompletePIN(ctx context.Context, requestToken, requestSecret, pin string) (string, string, error) {
	if requestToken == "" || requestSecret == "" || pin == "" {
		return "", "", errors.New("missing X PIN authorization data")
	}
	values, err := c.oauthCall(ctx, "/oauth/access_token", requestToken, requestSecret, map[string]string{"oauth_verifier": pin})
	if err != nil {
		return "", "", err
	}
	token, secret := values.Get("oauth_token"), values.Get("oauth_token_secret")
	if token == "" || secret == "" {
		return "", "", errors.New("X returned no user token")
	}
	return token, secret, nil
}
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
func (c Client) postJSON(ctx context.Context, path string, payload any, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err = c.sign(req); err != nil {
		return err
	}
	return c.do(req, result)
}
func (c Client) AuthenticatedUsername(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/2/users/me", nil)
	if err != nil {
		return "", err
	}
	if err = c.sign(req); err != nil {
		return "", err
	}
	var response struct {
		Data struct {
			Username string `json:"username"`
		} `json:"data"`
	}
	if err = c.do(req, &response); err != nil {
		return "", err
	}
	if response.Data.Username == "" {
		return "", errors.New("X returned no authenticated username")
	}
	return response.Data.Username, nil
}
func (c Client) VerifyPublisher(ctx context.Context, handle string) error {
	username, err := c.AuthenticatedUsername(ctx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(username, handle) {
		return fmt.Errorf("X access token belongs to @%s, expected @%s", username, handle)
	}
	return nil
}
func (c Client) Publish(ctx context.Context, body string, paths []string) (string, error) {
	if !c.CanPublish() {
		return "", errors.New("X user access token and secret are required for publication")
	}
	body = strings.TrimSpace(body)
	if (body == "" && len(paths) == 0) || utf8.RuneCountInString(body) > 280 {
		return "", errors.New("X post must contain media or 1..280 characters")
	}
	if len(paths) > 4 {
		return "", errors.New("X accepts at most four photos")
	}
	ids := make([]string, 0, len(paths))
	videoCount := 0
	for _, path := range paths {
		if strings.EqualFold(filepath.Ext(path), ".mp4") {
			videoCount++
		}
	}
	if videoCount > 1 || (videoCount == 1 && len(paths) > 1) {
		return "", errors.New("X supports one video or up to four photos per post")
	}
	for _, path := range paths {
		if videoCount == 1 {
			id, err := c.uploadVideo(ctx, path)
			if err != nil {
				return "", err
			}
			ids = append(ids, id)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if len(data) > 5<<20 {
			return "", errors.New("X photo exceeds 5 MiB")
		}
		mediaType := http.DetectContentType(data)
		if mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp" {
			return "", errors.New("unsupported X photo format")
		}
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := c.postJSON(ctx, "/2/media/upload", map[string]string{"media": base64.StdEncoding.EncodeToString(data), "media_category": "tweet_image"}, &response); err != nil {
			return "", fmt.Errorf("X image upload: %w", err)
		}
		if response.Data.ID == "" {
			return "", errors.New("X image upload returned no ID")
		}
		ids = append(ids, response.Data.ID)
	}
	payload := map[string]any{}
	if body != "" {
		payload["text"] = body
	}
	if len(ids) > 0 {
		payload["media"] = map[string]any{"media_ids": ids}
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.postJSON(ctx, "/2/tweets", payload, &response); err != nil {
		return "", err
	}
	if response.Data.ID == "" {
		return "", errors.New("X post creation returned no ID")
	}
	return response.Data.ID, nil
}
