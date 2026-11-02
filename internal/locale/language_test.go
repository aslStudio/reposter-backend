package locale

import "testing"

func TestNormalize(t *testing.T) {
	for input, want := range map[string]string{"": "ru", "RU": "ru", "en-us": "en-US", "tr": "tr"} {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"und", "not a language", "ru; ignore all instructions"} {
		if _, err := Normalize(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
