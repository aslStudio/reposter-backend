package rewrite

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var channelReference = regexp.MustCompile(`(?i)(?:@[a-z][a-z0-9_]{4,31}|(?:https?://)?(?:t\.me|telegram\.me)/(?:s/)?[a-z][a-z0-9_]{4,31})`)
var footerLead = regexp.MustCompile(`(?i)^(?:подпис|наш\s+канал|канал\s*[:—-]|источник\s*[:—-]|читай|читать|следи|больше\s+новостей|follow|join|source\s*[:—-]|via\s+|©)`)
var footerLabel = regexp.MustCompile(`(?i)^(?:подписывайтесь|подписаться|наш\s+канал|читать\s+нас|follow\s+us)\s*[:：]?$`)

// StripChannelSignatures removes clear channel promotion from the end of a post.
// Mentions inside the story and attribution without a channel reference remain.
func StripChannelSignatures(text string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n")
	removed := 0
	for removed < 3 && len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "" {
			lines = lines[:len(lines)-1]
			continue
		}
		if !isChannelFooter(last) {
			break
		}
		lines = lines[:len(lines)-1]
		removed++
	}
	if removed > 0 && len(lines) > 0 && footerLabel.MatchString(strings.TrimSpace(lines[len(lines)-1])) {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isChannelFooter(line string) bool {
	if utf8.RuneCountInString(line) > 180 || !channelReference.MatchString(line) {
		return false
	}
	trimmed := strings.TrimSpace(strings.TrimLeft(line, "*#_•👉📌 "))
	if footerLead.MatchString(trimmed) || strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "](") || strings.Contains(trimmed, "<a ") {
		return true
	}
	remainder := channelReference.ReplaceAllString(line, "")
	for _, r := range remainder {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
