package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxImageBytes = 10 << 20
const maxVideoBytes = 50 << 20

type MediaStore struct {
	Dir    string
	Client *http.Client
}

func (m MediaStore) Save(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || !allowedMediaHost(host) {
		return "", errors.New("image is not on an allowed media CDN")
	}
	if err = os.MkdirAll(m.Dir, 0700); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(rawURL))
	base := hex.EncodeToString(hash[:])
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" || !allowedMediaHost(strings.ToLower(req.URL.Hostname())) {
				return errors.New("image redirect left an allowed media CDN")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxImageBytes {
		return "", errors.New("image exceeds 10 MiB")
	}
	var ext string
	switch http.DetectContentType(data) {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	default:
		return "", errors.New("unsupported image format")
	}
	path := filepath.Join(m.Dir, base+ext)
	if _, err = os.Stat(path); err == nil {
		return path, nil
	}
	tmp, err := os.CreateTemp(m.Dir, ".image-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func (m MediaStore) SaveVideo(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || !allowedMediaHost(strings.ToLower(u.Hostname())) {
		return "", errors.New("video is not on an allowed media CDN")
	}
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return "", err
	}
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" || !allowedMediaHost(strings.ToLower(req.URL.Hostname())) {
				return errors.New("video redirect left an allowed media CDN")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("video download HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxVideoBytes {
		return "", errors.New("video exceeds 50 MiB")
	}
	tmp, err := os.CreateTemp(m.Dir, ".video-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = io.Copy(tmp, io.LimitReader(resp.Body, maxVideoBytes+1)); err != nil {
		return "", err
	}
	info, err := tmp.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > maxVideoBytes {
		return "", errors.New("video exceeds 50 MiB")
	}
	var header [12]byte
	if _, err = tmp.ReadAt(header[:], 0); err != nil || string(header[4:8]) != "ftyp" {
		return "", errors.New("unsupported video format: expected MP4")
	}
	if err = tmp.Chmod(0600); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(rawURL))
	path := filepath.Join(m.Dir, hex.EncodeToString(hash[:])+".mp4")
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func allowedMediaHost(host string) bool {
	return host == "telesco.pe" || strings.HasSuffix(host, ".telesco.pe") || strings.HasSuffix(host, ".cdn-telegram.org") || host == "pbs.twimg.com" || host == "video.twimg.com"
}
