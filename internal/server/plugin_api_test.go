package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TencentBlueKing/bk-plugin-framework-go/pluginapi"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

func TestPluginAPIDispatch(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)
	pluginapi.Register(func(router pluginapi.Router) {
		router.POST("/echo", func(w http.ResponseWriter, r *http.Request) {
			var payload store.JSONMap
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"username": r.Header.Get(auth.HeaderOperator),
				"payload":  payload,
			}))
		})
	})

	router, _ := newTestRouter(t)
	body := bytes.NewBufferString(`{
		"url": "/bk_plugin/plugin_api/echo",
		"method": "post",
		"username": "alice",
		"data": {"value": 1}
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/plugin_api_dispatch", body)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"username":"alice"`)
	require.Contains(t, rec.Body.String(), `"value":1`)
}

func TestPluginAPIDispatchForwardsPathParams(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)
	pluginapi.Register(func(router pluginapi.Router) {
		router.GET("/tasks/:id", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"id": pluginapi.Param(r, "id"),
			}))
		})
	})

	router, _ := newTestRouter(t)
	body := bytes.NewBufferString(`{
		"url": "/bk_plugin/plugin_api/tasks/42",
		"method": "get",
		"username": "alice"
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/plugin_api_dispatch", body)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"42"`)
}

func TestPluginAPIDispatchRejectsOutOfScopeURL(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)

	router, _ := newTestRouter(t)
	body := bytes.NewBufferString(`{
		"url": "/bk_plugin/meta",
		"method": "get",
		"username": "alice"
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/plugin_api_dispatch", body)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
