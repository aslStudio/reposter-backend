package locale

import (
	"errors"
	"strings"

	"golang.org/x/text/language"
)

// Normalize accepts a BCP 47 language tag and returns its canonical form.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "ru", nil
	}
	if len(raw) > 35 {
		return "", errors.New("invalid language tag")
	}
	tag, err := language.Parse(raw)
	if err != nil || tag == language.Und {
		return "", errors.New("invalid language tag")
	}
	base, _ := tag.Base()
	if base.String() == "und" {
		return "", errors.New("invalid language tag")
	}
	return tag.String(), nil
}
