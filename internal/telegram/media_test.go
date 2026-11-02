package telegram

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestMediaStoreSavesTelegramImage(t *testing.T) {
	image := append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("test-image")...)
	requests := 0
	store := MediaStore{Dir: t.TempDir(), Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(image))), Header: make(http.Header)}, nil
	})}}
	path, err := store.Save(context.Background(), "https://cdn4.telesco.pe/file/example")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(image) {
		t.Fatalf("saved image mismatch: %v", err)
	}
	if _, err = store.Save(context.Background(), "https://evil.example/file/example"); err == nil {
		t.Fatal("accepted non-Telegram host")
	}
	if requests != 1 {
		t.Fatalf("made %d requests", requests)
	}
}
