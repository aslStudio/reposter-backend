package x

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const videoChunkSize = 4 << 20
const maxVideoBytes = 50 << 20

type videoUploadResponse struct {
	Data struct {
		ID             string `json:"id"`
		ProcessingInfo *struct {
			State          string `json:"state"`
			CheckAfterSecs int    `json:"check_after_secs"`
			Error          *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"processing_info"`
	} `json:"data"`
}

func (c Client) uploadVideo(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() < 12 || info.Size() > maxVideoBytes {
		return "", errors.New("X video must be an MP4 of at most 50 MiB")
	}
	var header [12]byte
	if _, err := f.ReadAt(header[:], 0); err != nil || string(header[4:8]) != "ftyp" {
		return "", errors.New("X video must be MP4")
	}
	var init videoUploadResponse
	if err := c.postJSON(ctx, "/2/media/upload/initialize", map[string]any{"media_type": "video/mp4", "total_bytes": info.Size(), "media_category": "tweet_video"}, &init); err != nil {
		return "", fmt.Errorf("X video initialize: %w", err)
	}
	id := init.Data.ID
	if id == "" {
		return "", errors.New("X video initialize returned no ID")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	chunk := make([]byte, videoChunkSize)
	for segment := 0; ; segment++ {
		n, readErr := io.ReadFull(f, chunk)
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return "", readErr
		}
		if err := c.postJSON(ctx, "/2/media/upload/"+url.PathEscape(id)+"/append", map[string]any{"media": base64.StdEncoding.EncodeToString(chunk[:n]), "segment_index": segment}, nil); err != nil {
			return "", fmt.Errorf("X video segment %d: %w", segment, err)
		}
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	var done videoUploadResponse
	if err := c.postJSON(ctx, "/2/media/upload/"+url.PathEscape(id)+"/finalize", map[string]any{}, &done); err != nil {
		return "", fmt.Errorf("X video finalize: %w", err)
	}
	if done.Data.ID != "" && done.Data.ID != id {
		return "", errors.New("X video finalize returned a different ID")
	}
	for attempt := 0; attempt < 30; attempt++ {
		status := done.Data.ProcessingInfo
		if status == nil || status.State == "succeeded" {
			return id, nil
		}
		if status.State == "failed" {
			message := "processing failed"
			if status.Error != nil && status.Error.Message != "" {
				message = status.Error.Message
			}
			return "", fmt.Errorf("X video: %s", message)
		}
		if status.State != "pending" && status.State != "in_progress" {
			return "", fmt.Errorf("X video unexpected processing state %q", status.State)
		}
		wait := status.CheckAfterSecs
		if wait < 1 {
			wait = 1
		}
		if wait > 10 {
			wait = 10
		}
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		q := url.Values{"command": {"STATUS"}, "media_id": {id}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/2/media/upload?"+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		if err := c.sign(req); err != nil {
			return "", err
		}
		if err := c.do(req, &done); err != nil {
			return "", fmt.Errorf("X video status: %w", err)
		}
	}
	return "", errors.New("X video processing timed out")
}
