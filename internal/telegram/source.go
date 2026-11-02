package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var handleRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
var imageURLRE = regexp.MustCompile(`background-image\s*:\s*url\(["']?([^"')]+)`)

type PublicPost struct {
	ID          int64
	URL         string
	Text        string
	ImageURLs   []string
	PublishedAt time.Time
}

func ParseHandle(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "@") {
		raw = strings.TrimPrefix(raw, "@")
	}
	if handleRE.MatchString(raw) {
		return strings.ToLower(raw), nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || (u.Host != "t.me" && u.Host != "telegram.me") {
		return "", errors.New("expected a public https://t.me/channel link")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "s" {
		parts = parts[1:]
	}
	if len(parts) != 1 || !handleRE.MatchString(parts[0]) {
		return "", errors.New("expected a public channel username")
	}
	return strings.ToLower(parts[0]), nil
}

type Reader struct{ Client *http.Client }

func (r Reader) ReadSince(ctx context.Context, handle string, afterID int64) ([]PublicPost, error) {
	if !handleRE.MatchString(handle) {
		return nil, errors.New("invalid Telegram handle")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	var all []PublicPost
	before := int64(0)
	for page := 0; page < 20; page++ {
		pageURL := "https://t.me/s/" + handle
		if before != 0 {
			pageURL += "?before=" + strconv.FormatInt(before, 10)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Reposter/0.1)")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("Telegram source returned HTTP %d", resp.StatusCode)
		}
		posts, err := ParsePublicPage(io.LimitReader(resp.Body, 5<<20), handle)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(posts) == 0 {
			return all, nil
		}
		oldest := posts[0].ID
		for _, p := range posts {
			if p.ID < oldest {
				oldest = p.ID
			}
			if p.ID > afterID {
				all = append(all, p)
			}
		}
		if afterID == 0 || oldest <= afterID {
			return all, nil
		}
		if before != 0 && oldest >= before {
			return nil, errors.New("Telegram pagination did not move backward")
		}
		before = oldest
	}
	return nil, errors.New("Telegram backlog exceeds 20 pages; cursor left unchanged")
}

func ParsePublicPage(input io.Reader, handle string) ([]PublicPost, error) {
	root, err := html.Parse(input)
	if err != nil {
		return nil, err
	}
	var posts []PublicPost
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, "tgme_widget_message") {
			key := attr(n, "data-post")
			parts := strings.Split(key, "/")
			if len(parts) == 2 && strings.EqualFold(parts[0], handle) {
				id, parseErr := strconv.ParseInt(parts[1], 10, 64)
				if parseErr == nil {
					p := PublicPost{ID: id, URL: "https://t.me/" + handle + "/" + parts[1]}
					findContent(n, &p)
					posts = append(posts, p)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return posts, nil
}

func findContent(n *html.Node, p *PublicPost) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if hasClass(node, "tgme_widget_message_text") {
				p.Text = strings.TrimSpace(textContent(node))
				return
			}
			if hasClass(node, "tgme_widget_message_photo_wrap") {
				if match := imageURLRE.FindStringSubmatch(attr(node, "style")); len(match) == 2 {
					p.ImageURLs = append(p.ImageURLs, match[1])
				}
			}
			if node.Data == "time" {
				if ts, err := time.Parse(time.RFC3339, attr(node, "datetime")); err == nil {
					p.PublishedAt = ts
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
}
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		if node.Type == html.ElementNode && node.Data == "br" {
			b.WriteByte('\n')
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			if handle, err := ParseHandle(attr(node, "href")); err == nil {
				b.WriteString(" (https://t.me/")
				b.WriteString(handle)
				b.WriteByte(')')
			}
		}
		if node.Type == html.ElementNode && (node.Data == "p" || node.Data == "div") {
			b.WriteByte('\n')
		}
	}
	walk(n)
	return b.String()
}
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}
