package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-framework-go/kit"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type testPlugin struct{}

func (p testPlugin) Version() string { return "9.9.1" }
func (p testPlugin) Desc() string    { return "test plugin" }
func (p testPlugin) Execute(ctx *kit.Context) error {
	var inputs struct {
		Mode string `json:"mode"`
	}
	if err := ctx.ReadInputs(&inputs); err != nil {
		return err
	}
	if inputs.Mode == "poll" && ctx.InvokeCount() == 1 {
		ctx.WaitPoll(time.Millisecond)
		return nil
	}
	return ctx.WriteOutputs(map[string]interface{}{"mode": inputs.Mode, "count": ctx.InvokeCount()})
}

var installTestPluginOnce sync.Once

func newTestRouter(t *testing.T) (*gin.Engine, *store.GormStore) {
	return newTestRouterWithOptions(t, hub.Options{})
}

func newTestRouterWithOptions(t *testing.T, opts hub.Options) (*gin.Engine, *store.GormStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	hub.Configure(opts)
	t.Cleanup(func() {
		hub.Configure(hub.Options{})
	})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := store.NewGormStore(db)
	require.NoError(t, s.AutoMigrate(context.Background()))
	installTestPluginOnce.Do(func() {
		hub.MustInstallV2(testPlugin{}, hub.PluginSpec{
			Inputs: struct {
				Mode string `json:"mode"`
			}{},
			Outputs: struct {
				Mode string `json:"mode"`
			}{},
			Form: []byte(`{"mode":{"component":"input"}}`),
		})
	})
	return NewRouter(Config{Store: s, Logger: logrus.NewEntry(logrus.StandardLogger())}), s
}

func TestMetaAndDetail(t *testing.T) {
	router, _ := newTestRouter(t)

	meta := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bk_plugin/meta", nil)
	router.ServeHTTP(meta, req)
	require.Equal(t, http.StatusOK, meta.Code)
	require.Contains(t, meta.Body.String(), "9.9.1")

	detail := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/bk_plugin/detail/9.9.1", nil)
	router.ServeHTTP(detail, req)
	require.Equal(t, http.StatusOK, detail.Code)
	require.Contains(t, detail.Body.String(), "renderform")
	require.Contains(t, detail.Body.String(), "test plugin")
}

func TestInvokeSyncAndScheduleRead(t *testing.T) {
	router, _ := newTestRouter(t)

	body := bytes.NewBufferString(`{"inputs":{"mode":"sync"},"context":{}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/invoke/9.9.1", body)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Data struct {
			TraceID string `json:"trace_id"`
			State   int    `json:"state"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Data.TraceID)
	require.Equal(t, 4, payload.Data.State)

	schedule := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/bk_plugin/schedule/"+payload.Data.TraceID, nil)
	router.ServeHTTP(schedule, req)
	require.Equal(t, http.StatusOK, schedule.Code)
	require.Contains(t, schedule.Body.String(), `"mode":"sync"`)
}

func TestInvokeScopeDenied(t *testing.T) {
	router, _ := newTestRouterWithOptions(t, hub.Options{
		AllowScope: hub.AllowScope{
			"bk_sops": {Type: "project", Value: []string{"1"}},
		},
	})

	body := bytes.NewBufferString(`{"inputs":{"mode":"sync"},"context":{}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/invoke/9.9.1", body)
	req.Header.Set("X-Bkapi-App-Code", "bk_sops")
	req.Header.Set("Bkplugin-Scope-Type", "project")
	req.Header.Set("Bkplugin-Scope-Value", "2")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestInvokeFinishCallback(t *testing.T) {
	var got store.JSONMap
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"result": true}`))
	}))
	defer callbackServer.Close()

	router, _ := newTestRouterWithOptions(t, hub.Options{EnablePluginCallback: true})

	body := bytes.NewBufferString(`{
		"inputs":{"mode":"sync"},
		"context":{
			"plugin_callback_info":{
				"url":"` + callbackServer.URL + `",
				"data":{"trace_id":"from-caller"}
			}
		}
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/invoke/9.9.1", body)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, store.JSONMap{"trace_id": "from-caller"}, got)
}
