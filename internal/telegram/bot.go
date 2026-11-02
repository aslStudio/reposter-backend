package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Bot struct {
	Token  string
	Client *http.Client
}

type PublishOptions struct {
	FormattingEnabled bool
	SignatureText     string
}

type botResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (b Bot) Publish(ctx context.Context, chatID, text string, paths []string, options PublishOptions) (string, error) {
	if b.Token == "" {
		return "", errors.New("Telegram bot token is not configured")
	}
	if strings.TrimSpace(text) == "" && len(paths) == 0 {
		return "", errors.New("empty post")
	}
	formatted, visibleLength, err := renderPublication(text, chatID, options)
	if err != nil {
		return "", err
	}
	if visibleLength > 4096 {
		return "", errors.New("post text exceeds Telegram limit")
	}
	if len(paths) > 10 {
		return "", errors.New("Telegram album exceeds 10 media items")
	}
	if len(paths) == 0 {
		return b.sendText(ctx, chatID, formatted)
	}
	caption := formatted
	if visibleLength > 1024 {
		caption = ""
	}
	var id string
	if len(paths) == 1 {
		if strings.EqualFold(filepath.Ext(paths[0]), ".mp4") {
			id, err = b.sendVideo(ctx, chatID, paths[0], caption)
		} else {
			id, err = b.sendPhoto(ctx, chatID, paths[0], caption)
		}
	} else {
		id, err = b.sendAlbum(ctx, chatID, paths, caption)
	}
	if err != nil {
		return "", err
	}
	if caption == "" && formatted != "" {
		if _, err = b.sendText(ctx, chatID, formatted); err != nil {
			return id, fmt.Errorf("media published but text failed: %w", err)
		}
	}
	return id, nil
}
func (b Bot) sendText(ctx context.Context, chatID, text string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true})
	resp, err := b.call(ctx, "sendMessage", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	return firstMessageID(resp.Result)
}
func (b Bot) sendPhoto(ctx context.Context, chatID, path, caption string) (string, error) {
	var data bytes.Buffer
	w := multipart.NewWriter(&data)
	_ = w.WriteField("chat_id", chatID)
	if caption != "" {
		_ = w.WriteField("caption", caption)
		_ = w.WriteField("parse_mode", "HTML")
	}
	if err := addFile(w, "photo", path); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	resp, err := b.call(ctx, "sendPhoto", w.FormDataContentType(), &data)
	if err != nil {
		return "", err
	}
	return firstMessageID(resp.Result)
}
func (b Bot) sendVideo(ctx context.Context, chatID, path, caption string) (string, error) {
	resp, err := b.callMultipart(ctx, "sendVideo", func(w *multipart.Writer) error {
		if err := w.WriteField("chat_id", chatID); err != nil {
			return err
		}
		if err := w.WriteField("supports_streaming", "true"); err != nil {
			return err
		}
		if caption != "" {
			if err := w.WriteField("caption", caption); err != nil {
				return err
			}
			if err := w.WriteField("parse_mode", "HTML"); err != nil {
				return err
			}
		}
		return addFile(w, "video", path)
	})
	if err != nil {
		return "", err
	}
	return firstMessageID(resp.Result)
}
func (b Bot) sendAlbum(ctx context.Context, chatID string, paths []string, caption string) (string, error) {
	resp, err := b.callMultipart(ctx, "sendMediaGroup", func(w *multipart.Writer) error {
		if err := w.WriteField("chat_id", chatID); err != nil {
			return err
		}
		media := make([]map[string]string, 0, len(paths))
		for i, path := range paths {
			name := "file" + strconv.Itoa(i)
			kind := "photo"
			if strings.EqualFold(filepath.Ext(path), ".mp4") {
				kind = "video"
			}
			entry := map[string]string{"type": kind, "media": "attach://" + name}
			if i == 0 && caption != "" {
				entry["caption"], entry["parse_mode"] = caption, "HTML"
			}
			media = append(media, entry)
		}
		encoded, err := json.Marshal(media)
		if err != nil {
			return err
		}
		if err := w.WriteField("media", string(encoded)); err != nil {
			return err
		}
		for i, path := range paths {
			if err := addFile(w, "file"+strconv.Itoa(i), path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return firstMessageID(resp.Result)
}
func (b Bot) callMultipart(ctx context.Context, method string, write func(*multipart.Writer) error) (botResponse, error) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		err := write(multipartWriter)
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	resp, err := b.call(ctx, method, multipartWriter.FormDataContentType(), reader)
	_ = reader.Close()
	return resp, err
}
func addFile(w *multipart.Writer, field, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	part, err := w.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, f)
	return err
}
func (b Bot) call(ctx context.Context, method, contentType string, body io.Reader) (botResponse, error) {
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.Token+"/"+method, body)
	if err != nil {
		return botResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)
	res, err := client.Do(req)
	if err != nil {
		return botResponse{}, errors.New("Telegram API request failed")
	}
	defer res.Body.Close()
	var out botResponse
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return out, err
	}
	if !out.OK {
		return out, fmt.Errorf("Telegram API HTTP %d: %s", res.StatusCode, out.Description)
	}
	return out, nil
}
func firstMessageID(raw json.RawMessage) (string, error) {
	var one struct {
		MessageID int64 `json:"message_id"`
	}
	if len(raw) > 0 && raw[0] == '[' {
		var many []struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(raw, &many); err != nil {
			return "", err
		}
		if len(many) == 0 {
			return "", errors.New("Telegram returned an empty media group")
		}
		return strconv.FormatInt(many[0].MessageID, 10), nil
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		return "", err
	}
	if one.MessageID == 0 {
		return "", errors.New("Telegram returned no message ID")
	}
	return strconv.FormatInt(one.MessageID, 10), nil
}
