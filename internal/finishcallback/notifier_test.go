package finishcallback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

func TestParse(t *testing.T) {
	info, ok := Parse(store.JSONMap{
		"plugin_callback_info": map[string]interface{}{
			"url":  "http://example.com/callback",
			"data": map[string]interface{}{"trace_id": "trace-1"},
		},
	})
	require.True(t, ok)
	require.Equal(t, "http://example.com/callback", info.URL)
	require.Equal(t, store.JSONMap{"trace_id": "trace-1"}, info.Data)
}

func TestNotifyPostsCallbackData(t *testing.T) {
	var got store.JSONMap
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"result": true}`))
	}))
	defer server.Close()

	err := Notify(context.Background(), server.Client(), Info{
		URL:  server.URL,
		Data: store.JSONMap{"done": true},
	})
	require.NoError(t, err)
	require.Equal(t, store.JSONMap{"done": true}, got)
}
