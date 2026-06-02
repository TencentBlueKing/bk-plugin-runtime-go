package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-framework-go/kit"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/scheduler"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/server"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

const (
	sopsSyncVersion     = "10.0.0"
	sopsPollVersion     = "10.0.1"
	sopsCallbackVersion = "10.0.2"
)

var installSOPSE2EPluginsOnce sync.Once

type sopsInputs struct {
	TemplateID int    `json:"template_id"`
	TaskName   string `json:"task_name"`
}

type sopsSyncPlugin struct{}

func (p sopsSyncPlugin) Version() string { return sopsSyncVersion }
func (p sopsSyncPlugin) Desc() string    { return "sops sync e2e plugin" }
func (p sopsSyncPlugin) Execute(ctx *kit.Context) error {
	inputs, err := readSOPSInputs(ctx)
	if err != nil {
		return err
	}
	return writeSOPSOutputs(ctx, inputs, "sync-job-001")
}

type sopsPollPlugin struct{}

func (p sopsPollPlugin) Version() string { return sopsPollVersion }
func (p sopsPollPlugin) Desc() string    { return "sops poll e2e plugin" }
func (p sopsPollPlugin) Execute(ctx *kit.Context) error {
	inputs, err := readSOPSInputs(ctx)
	if err != nil {
		return err
	}
	if ctx.InvokeCount() == 1 {
		ctx.WaitPoll(time.Millisecond)
		return nil
	}
	return writeSOPSOutputs(ctx, inputs, "poll-job-001")
}

type sopsCallbackPlugin struct{}

func (p sopsCallbackPlugin) Version() string { return sopsCallbackVersion }
func (p sopsCallbackPlugin) Desc() string    { return "sops callback e2e plugin" }
func (p sopsCallbackPlugin) Execute(ctx *kit.Context) error {
	inputs, err := readSOPSInputs(ctx)
	if err != nil {
		return err
	}
	if ctx.InvokeCount() == 1 {
		ctx.WaitCallback(10 * time.Minute)
		return nil
	}

	var callbackPayload struct {
		Result bool `json:"result"`
		Data   struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	if err := ctx.ReadCallback(&callbackPayload); err != nil {
		return err
	}
	if !callbackPayload.Result {
		return fmt.Errorf("sops callback result is false")
	}
	return writeSOPSOutputs(ctx, inputs, callbackPayload.Data.JobID)
}

func TestSOPSInvokeSyncPluginFlow(t *testing.T) {
	ctx := context.Background()
	env := newSOPSE2EEnv(t)
	finishCallback := newFinishCallbackServer(t)
	defer finishCallback.Close()

	invokeResp := invokeFromSOPS(t, env.Router, sopsSyncVersion, finishCallback.URL)
	require.Equal(t, constants.StateSuccess, invokeResp.State)
	require.Empty(t, invokeResp.CallbackURL)

	scheduleResp := getScheduleFromSOPS(t, env.Router, invokeResp.TraceID)
	require.Equal(t, constants.StateSuccess, scheduleResp.State)
	require.Equal(t, "sync-job-001", scheduleResp.Outputs["job_id"])
	requireScheduleAudit(t, ctx, env.Store, invokeResp.TraceID)
	requireFinishCallback(t, finishCallback.C, "sops-sync-plugin")
}

func TestSOPSInvokePollPluginFlow(t *testing.T) {
	ctx := context.Background()
	env := newSOPSE2EEnv(t)
	finishCallback := newFinishCallbackServer(t)
	defer finishCallback.Close()

	invokeResp := invokeFromSOPS(t, env.Router, sopsPollVersion, finishCallback.URL)
	require.Equal(t, constants.StatePoll, invokeResp.State)

	waitingSchedule := getScheduleFromSOPS(t, env.Router, invokeResp.TraceID)
	require.Equal(t, constants.StatePoll, waitingSchedule.State)

	time.Sleep(2 * time.Millisecond)
	runWorkerOnce(t, ctx, env.Store)

	finalSchedule := getScheduleFromSOPS(t, env.Router, invokeResp.TraceID)
	require.Equal(t, constants.StateSuccess, finalSchedule.State)
	require.Equal(t, "poll-job-001", finalSchedule.Outputs["job_id"])
	requireScheduleAudit(t, ctx, env.Store, invokeResp.TraceID)
	requireFinishCallback(t, finishCallback.C, "sops-poll-plugin")
}

func TestSOPSInvokeCallbackPluginFlow(t *testing.T) {
	ctx := context.Background()
	env := newSOPSE2EEnv(t)
	finishCallback := newFinishCallbackServer(t)
	defer finishCallback.Close()

	invokeResp := invokeFromSOPS(t, env.Router, sopsCallbackVersion, finishCallback.URL)
	require.Equal(t, constants.StateCallback, invokeResp.State)
	require.NotEmpty(t, invokeResp.CallbackURL)

	waitingSchedule := getScheduleFromSOPS(t, env.Router, invokeResp.TraceID)
	require.Equal(t, constants.StateCallback, waitingSchedule.State)

	callbackRec := httptest.NewRecorder()
	callbackReq := httptest.NewRequest(http.MethodPost, invokeResp.CallbackURL, bytes.NewBufferString(`{
		"result": true,
		"data": {
			"job_id": "callback-job-001"
		}
	}`))
	env.Router.ServeHTTP(callbackRec, callbackReq)
	require.Equal(t, http.StatusOK, callbackRec.Code, callbackRec.Body.String())

	runWorkerOnce(t, ctx, env.Store)

	finalSchedule := getScheduleFromSOPS(t, env.Router, invokeResp.TraceID)
	require.Equal(t, constants.StateSuccess, finalSchedule.State)
	require.Equal(t, "callback-job-001", finalSchedule.Outputs["job_id"])
	requireScheduleAudit(t, ctx, env.Store, invokeResp.TraceID)
	requireFinishCallback(t, finishCallback.C, "sops-callback-plugin")
}

func readSOPSInputs(ctx *kit.Context) (sopsInputs, error) {
	var inputs sopsInputs
	return inputs, ctx.ReadInputs(&inputs)
}

func writeSOPSOutputs(ctx *kit.Context, inputs sopsInputs, jobID string) error {
	return ctx.WriteOutputs(map[string]interface{}{
		"template_id":  inputs.TemplateID,
		"task_name":    inputs.TaskName,
		"job_id":       jobID,
		"invoke_count": ctx.InvokeCount(),
	})
}

func installSOPSE2EPlugins() {
	spec := hub.PluginSpec{
		Inputs: sopsInputs{},
		ContextInputs: struct {
			BkBizID int `json:"bk_biz_id"`
		}{},
		Outputs: struct {
			JobID string `json:"job_id"`
		}{},
		Form: []byte(`{"template_id":{"component":"input"},"task_name":{"component":"input"}}`),
	}
	hub.MustInstallV2(sopsSyncPlugin{}, spec)
	hub.MustInstallV2(sopsPollPlugin{}, spec)
	hub.MustInstallV2(sopsCallbackPlugin{}, spec)
}

type sopsE2EEnv struct {
	Router *gin.Engine
	Store  *store.GormStore
}

func newSOPSE2EEnv(t *testing.T) sopsE2EEnv {
	t.Helper()
	t.Setenv("BK_PLUGIN_CALLBACK_TOKEN_SECRET", "test-callback-secret")
	gin.SetMode(gin.TestMode)
	hub.Configure(hub.Options{
		AllowScope: hub.AllowScope{
			"bk_sops": {Type: "project", Value: []string{"42"}},
		},
		EnablePluginCallback: true,
	})
	t.Cleanup(func() {
		hub.Configure(hub.Options{})
	})
	installSOPSE2EPluginsOnce.Do(installSOPSE2EPlugins)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := store.NewGormStore(db)
	require.NoError(t, s.AutoMigrate(context.Background()))
	return sopsE2EEnv{
		Router: server.NewRouter(server.Config{Store: s, Logger: logrus.NewEntry(logrus.StandardLogger())}),
		Store:  s,
	}
}

type finishCallbackServer struct {
	URL string
	C   <-chan store.JSONMap
	srv *httptest.Server
}

func (s finishCallbackServer) Close() {
	s.srv.Close()
}

func newFinishCallbackServer(t *testing.T) finishCallbackServer {
	t.Helper()
	ch := make(chan store.JSONMap, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload store.JSONMap
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		ch <- payload
		_, _ = w.Write([]byte(`{"result": true}`))
	}))
	return finishCallbackServer{URL: srv.URL, C: ch, srv: srv}
}

type invokeResponse struct {
	TraceID     string
	State       constants.State
	CallbackURL string
}

func invokeFromSOPS(t *testing.T, router *gin.Engine, version string, finishCallbackURL string) invokeResponse {
	t.Helper()
	body := bytes.NewBufferString(`{
		"inputs": {
			"template_id": 1001,
			"task_name": "` + taskName(version) + `"
		},
		"context": {
			"bk_biz_id": 42,
			"plugin_callback_info": {
				"url": "` + finishCallbackURL + `",
				"data": {
					"sops_task_id": "` + taskName(version) + `",
					"callback_source": "bk_sops"
				}
			}
		}
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bk_plugin/invoke/"+version, body)
	setSOPSHeaders(req)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload struct {
		Data struct {
			TraceID     string          `json:"trace_id"`
			State       constants.State `json:"state"`
			CallbackURL string          `json:"callback_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Data.TraceID)
	return invokeResponse{
		TraceID:     payload.Data.TraceID,
		State:       payload.Data.State,
		CallbackURL: payload.Data.CallbackURL,
	}
}

type scheduleResponse struct {
	State   constants.State
	Outputs store.JSONMap
}

func getScheduleFromSOPS(t *testing.T, router *gin.Engine, traceID string) scheduleResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bk_plugin/schedule/"+traceID, nil)
	setSOPSHeaders(req)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload struct {
		Data struct {
			State   constants.State `json:"state"`
			Outputs store.JSONMap   `json:"outputs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return scheduleResponse{State: payload.Data.State, Outputs: payload.Data.Outputs}
}

func runWorkerOnce(t *testing.T, ctx context.Context, s store.ScheduleStore) {
	t.Helper()
	worker := scheduler.NewWorker(scheduler.Config{
		Store:    s,
		WorkerID: "sops-e2e-worker",
		Limit:    10,
		LockFor:  time.Minute,
		Logger:   logrus.NewEntry(logrus.StandardLogger()),
	})
	require.NoError(t, worker.RunOnce(ctx))
}

func requireScheduleAudit(t *testing.T, ctx context.Context, s store.ScheduleStore, traceID string) {
	t.Helper()
	saved, err := s.Get(ctx, traceID)
	require.NoError(t, err)
	require.Equal(t, "bk_sops", saved.CallerApp)
	require.Equal(t, "admin", saved.Operator)
	require.Equal(t, "req-sops-e2e", saved.RequestID)
	require.Equal(t, "system", saved.TenantID)
}

func requireFinishCallback(t *testing.T, ch <-chan store.JSONMap, taskID string) {
	t.Helper()
	select {
	case got := <-ch:
		require.Equal(t, store.JSONMap{"sops_task_id": taskID, "callback_source": "bk_sops"}, got)
	case <-time.After(time.Second):
		t.Fatal("expected SOPS finish callback")
	}
}

func setSOPSHeaders(req *http.Request) {
	req.Header.Set(auth.HeaderAppCode, "bk_sops")
	req.Header.Set(auth.HeaderOperator, "admin")
	req.Header.Set(auth.HeaderRequestID, "req-sops-e2e")
	req.Header.Set(auth.HeaderTenantID, "system")
	req.Header.Set(auth.HeaderScopeType, "project")
	req.Header.Set(auth.HeaderScopeValue, "42")
}

func taskName(version string) string {
	switch version {
	case sopsSyncVersion:
		return "sops-sync-plugin"
	case sopsPollVersion:
		return "sops-poll-plugin"
	case sopsCallbackVersion:
		return "sops-callback-plugin"
	default:
		return "sops-plugin"
	}
}
