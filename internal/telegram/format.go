package telegram

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"unicode/utf8"
)

func renderPublication(text, chatID string, options PublishOptions) (string, int, error) {
	visible := text
	formatted := html.EscapeString(text)
	if options.FormattingEnabled {
		formatted = renderHTML(text)
	}
	if options.SignatureText != "" {
		if !strings.HasPrefix(chatID, "@") {
			return "", 0, fmt.Errorf("signature requires a public Telegram channel username")
		}
		handle, err := ParseHandle(chatID)
		if err != nil {
			return "", 0, err
		}
		separator := "\n\n"
		if strings.TrimSpace(text) == "" {
			separator = ""
		}
		visible += separator + options.SignatureText
		formatted += separator + "<a href=\"https://t.me/" + handle + "\">" + html.EscapeString(options.SignatureText) + "</a>"
	}
	return formatted, utf8.RuneCountInString(visible), nil
}

func renderHTML(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if title, ok := markdownHeading(line); ok {
			lines[i] = "<b>" + renderInline(title) + "</b>"
			continue
		}
		if strings.HasPrefix(line, "> ") {
			lines[i] = "<blockquote>" + renderInline(strings.TrimPrefix(line, "> ")) + "</blockquote>"
			continue
		}
		lines[i] = renderInline(line)
	}
	return strings.Join(lines, "\n")
}

func markdownHeading(line string) (string, bool) {
	count := 0
	for count < len(line) && count < 6 && line[count] == '#' {
		count++
	}
	if count == 0 || count >= len(line) || line[count] != ' ' {
		return "", false
	}
	title := strings.TrimSpace(line[count+1:])
	if title == "" {
		return "", false
	}
	if strings.HasPrefix(title, "**") && strings.HasSuffix(title, "**") && len(title) > 4 {
		title = title[2 : len(title)-2]
	}
	return title, true
}

func renderInline(line string) string {
	var result strings.Builder
	for len(line) > 0 {
		if strings.HasPrefix(line, "[") {
			if closeLabel := strings.Index(line, "]("); closeLabel > 1 {
				if closeURL := strings.IndexByte(line[closeLabel+2:], ')'); closeURL > 0 {
					link := line[closeLabel+2 : closeLabel+2+closeURL]
					if u, err := url.Parse(link); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
						result.WriteString("<a href=\"")
						result.WriteString(html.EscapeString(link))
						result.WriteString("\">")
						result.WriteString(html.EscapeString(line[1:closeLabel]))
						result.WriteString("</a>")
						line = line[closeLabel+2+closeURL+1:]
						continue
					}
				}
			}
		}
		matched := false
		for _, item := range []struct{ marker, tag string }{
			{"**", "b"}, {"__", "u"}, {"~~", "s"}, {"||", "tg-spoiler"}, {"`", "code"}, {"*", "i"},
		} {
			if !strings.HasPrefix(line, item.marker) {
				continue
			}
			end := strings.Index(line[len(item.marker):], item.marker)
			if end <= 0 {
				continue
			}
			inside := line[len(item.marker) : len(item.marker)+end]
			if item.marker == "*" && strings.HasPrefix(inside, "*") {
				continue
			}
			result.WriteString("<" + item.tag + ">")
			if item.tag == "code" {
				result.WriteString(html.EscapeString(inside))
			} else {
				result.WriteString(renderInline(inside))
			}
			result.WriteString("</" + item.tag + ">")
			line = line[len(item.marker)+end+len(item.marker):]
			matched = true
			break
		}
		if matched {
			continue
		}
		_, size := utf8.DecodeRuneInString(line)
		result.WriteString(html.EscapeString(line[:size]))
		line = line[size:]
	}
	return result.String()
}
