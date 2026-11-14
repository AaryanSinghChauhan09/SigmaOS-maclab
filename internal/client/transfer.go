package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/upload"
)

// errStartOver is a partial file that is not the start of what labd serves.
var errStartOver = errors.New("the partial file is not this download's")

// fetch downloads path into dst through dst.part and returns the file's
// sha256. A cut download asks again for the bytes it lacks, up to eight
// times in a row without a byte arriving; the partial file stays for the
// next call when it gives up.
func (c *Client) fetch(ctx context.Context, path, name, dst string) (string, error) {
	part := dst + ".part"
	for try := 0; ; try++ {
		if try > 0 {
			pause := c.pause
			if pause == nil {
				pause = upload.Backoff
			}
			if err := pause(ctx, try); err != nil {
				return "", err
			}
		}
		before := size(part)
		err := c.fetchOnce(ctx, path, part)
		if err == nil {
			break
		}
		var no *StatusError
		switch {
		case errors.As(err, &no):
			return "", fmt.Errorf("%s: %w", name, err)
		case errors.Is(err, errStartOver) || size(part) > before:
			try = 0
		case try >= upload.Retries || ctx.Err() != nil:
			return "", fmt.Errorf("%s: %w", name, err)
		}
	}
	f, err := os.Open(part)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), os.Rename(part, dst)
}

func size(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// fetchOnce appends to part what labd serves from part's length on. A
// StatusError is labd refusing: asking again cannot help.
func (c *Client) fetchOnce(ctx context.Context, path, part string) error {
	have := size(part)
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_WRONLY | os.O_CREATE
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		flags |= os.O_TRUNC
	case http.StatusRequestedRangeNotSatisfiable:
		// Nothing lies past the partial file: it is whole, or it is longer
		// than the file and so another's.
		if resp.Header.Get("Content-Range") == fmt.Sprintf("bytes */%d", have) {
			return nil
		}
		if err := os.Remove(part); err != nil {
			return err
		}
		return errStartOver
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		msg := strings.TrimSpace(string(b))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return &StatusError{Code: resp.StatusCode, Msg: msg}
		}
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	return errors.Join(err, f.Close())
}
