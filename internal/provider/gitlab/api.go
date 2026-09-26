package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var ErrTokenRejected = errors.New("gitlab rejected the runner token")

type Client struct {
	HTTP *http.Client
}

type RunnerInfo struct {
	ID int64 `json:"id"`
}

func (c *Client) Verify(ctx context.Context, baseURL, token string) (RunnerInfo, error) {
	res, body, err := c.postToken(ctx, http.MethodPost, baseURL+"/api/v4/runners/verify", token)
	if err != nil {
		return RunnerInfo{}, err
	}
	switch res {
	case http.StatusOK:
		var info RunnerInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return RunnerInfo{}, fmt.Errorf("decode verify response: %w", err)
		}
		return info, nil
	case http.StatusForbidden, http.StatusUnauthorized:
		return RunnerInfo{}, ErrTokenRejected
	default:
		return RunnerInfo{}, fmt.Errorf("gitlab verify returned %d: %s", res, summarize(body))
	}
}

func (c *Client) Delete(ctx context.Context, baseURL, token string) error {
	res, body, err := c.postToken(ctx, http.MethodDelete, baseURL+"/api/v4/runners", token)
	if err != nil {
		return err
	}
	switch res {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusNotFound:
		return ErrTokenRejected
	default:
		return fmt.Errorf("gitlab delete returned %d: %s", res, summarize(body))
	}
}

func (c *Client) postToken(ctx context.Context, method, endpoint, token string) (int, []byte, error) {
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("gitlab request failed: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, body, errors.Join(readErr, resp.Body.Close())
}

func summarize(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
