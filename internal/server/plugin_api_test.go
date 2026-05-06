package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/pluginapi"
)

func TestPluginAPIDispatch(t *testing.T) {
	pluginapi.ResetForTest()
	t.Cleanup(pluginapi.ResetForTest)
	pluginapi.RegisterGin(func(router gin.IRouter) {
		router.POST("/echo", func(c *gin.Context) {
			var payload store.JSONMap
			require.NoError(t, c.ShouldBindJSON(&payload))
			c.JSON(http.StatusOK, gin.H{"username": c.GetHeader(auth.HeaderOperator), "payload": payload})
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
