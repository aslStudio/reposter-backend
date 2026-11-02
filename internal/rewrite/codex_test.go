package rewrite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexPassesCLIKey(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$CODEX_API_KEY\" = test-key ] || exit 20\n" +
		"[ -z \"$OPENAI_API_KEY\" ] || exit 21\n" +
		"found=\nwhile IFS= read -r line; do case \"$line\" in *'Язык итоговой публикации (BCP 47): en'*) found=yes;; esac; done\n" +
		"[ \"$found\" = yes ] || exit 22\n" +
		"printf 'rewritten post\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "home"))
	result, err := (Codex{APIKey: "test-key"}).Rewrite(context.Background(), "source post", "news style", "en")
	if err != nil {
		t.Fatal(err)
	}
	if result != "rewritten post" {
		t.Fatalf("unexpected rewrite: %q", result)
	}
}
