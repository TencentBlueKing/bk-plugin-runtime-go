package finishcallback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

const (
	contextKey  = "plugin_callback_info"
	retryTimes  = 3
	retryDelay  = 500 * time.Millisecond
	httpTimeout = 10 * time.Second
)

type Info struct {
	URL  string        `json:"url"`
	Data store.JSONMap `json:"data"`
}

func Parse(contextInputs store.JSONMap) (Info, bool) {
	raw, ok := contextInputs[contextKey]
	if !ok || raw == nil {
		return Info{}, false
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return Info{}, false
	}

	var info Info
	if err := json.Unmarshal(data, &info); err != nil || info.URL == "" {
		return Info{}, false
	}
	if info.Data == nil {
		info.Data = store.JSONMap{}
	}
	return info, true
}

func NotifyWithRetry(ctx context.Context, client *http.Client, info Info) error {
	if info.URL == "" {
		return nil
	}
	// Always use a client with an explicit per-request timeout so that a slow
	// or unreachable finish-callback URL cannot stall the caller indefinitely.
	timeoutClient := &http.Client{Timeout: httpTimeout}
	if client != nil && client != http.DefaultClient {
		// Honour a custom transport from the caller, but still enforce the timeout.
		timeoutClient = &http.Client{Transport: client.Transport, Timeout: httpTimeout}
	}

	var lastErr error
	for i := 0; i < retryTimes; i++ {
		if err := Notify(ctx, timeoutClient, info); err != nil {
			lastErr = err
			if i == retryTimes-1 {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
			continue
		}
		return nil
	}
	return lastErr
}

func Notify(ctx context.Context, client *http.Client, info Info) error {
	if info.URL == "" {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}

	body, err := json.Marshal(info.Data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, info.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("plugin finish callback status %d", resp.StatusCode)
	}

	var payload struct {
		Result *bool `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if payload.Result != nil && !*payload.Result {
		return fmt.Errorf("plugin finish callback result false")
	}
	return nil
}
