package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TencentBlueKing/bk-plugin-framework-go/pluginapi"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

func TestPluginAPIDispatchPassesThroughPluginAPIResponse(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)
	pluginapi.Register(func(router pluginapi.Router) {
		router.GET("/accounts/list", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"result":  true,
				"message": "success",
				"data": []map[string]string{
					{"label": "ziyan-hcm-test", "value": "0000002b"},
				},
			}))
		})
	})

	router, _ := newTestRouter(t)
	body := bytes.NewBufferString(`{
		"url": "/bk_plugin/plugin_api/accounts/list?vendor=tcloud-ziyan",
		"method": "get",
		"username": "alice"
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/plugin_api_dispatch", body)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.JSONEq(t, `{
		"result": true,
		"message": "success",
		"data": [{"label": "ziyan-hcm-test", "value": "0000002b"}]
	}`, rec.Body.String())
}

func TestPluginAPIDispatchMatchesTrailingSlashVariant(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)
	pluginapi.Register(func(router pluginapi.Router) {
		router.GET("/accounts/list", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"vendor":    r.URL.Query().Get("vendor"),
				"bk_biz_id": r.URL.Query().Get("bk_biz_id"),
			}))
		})
	})

	router, _ := newTestRouter(t)
	body := bytes.NewBufferString(`{
		"url": "/bk_plugin/plugin_api/accounts/list/?bk_biz_id=213&vendor=tcloud-ziyan",
		"method": "get",
		"username": "alice"
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/plugin_api_dispatch", body)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"vendor":"tcloud-ziyan","bk_biz_id":"213"}`, rec.Body.String())
}

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

func TestPluginAPIDispatchForwardsMultipartFiles(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)
	pluginapi.Register(func(router pluginapi.Router) {
		router.POST("/upload", func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, r.ParseMultipartForm(32<<20))
			file, header, err := r.FormFile("cert")
			require.NoError(t, err)
			defer file.Close()
			content, err := io.ReadAll(file)
			require.NoError(t, err)

			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{
				"username":  r.Header.Get(auth.HeaderOperator),
				"vendor":    r.FormValue("vendor"),
				"bk_biz_id": r.FormValue("bk_biz_id"),
				"filename":  header.Filename,
				"content":   string(content),
			}))
		})
	})

	router, _ := newTestRouter(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("url", "/bk_plugin/plugin_api/upload"))
	require.NoError(t, writer.WriteField("method", "post"))
	require.NoError(t, writer.WriteField("username", "alice"))
	require.NoError(t, writer.WriteField("dumped_data", `{"vendor":"tcloud","bk_biz_id":213}`))
	part, err := writer.CreateFormFile("cert", "cert.txt")
	require.NoError(t, err)
	_, err = part.Write([]byte("cert-content"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/plugin_api_dispatch", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{
		"username": "alice",
		"vendor": "tcloud",
		"bk_biz_id": "213",
		"filename": "cert.txt",
		"content": "cert-content"
	}`, rec.Body.String())
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
