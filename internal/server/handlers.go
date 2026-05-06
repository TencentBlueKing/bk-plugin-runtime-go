package server

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-framework-go/executor"
	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/callback"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/finishcallback"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/httpx"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/runtimeadapter"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/version"
)

type Handler struct {
	store  store.ScheduleStore
	logger *logrus.Entry
	engine *gin.Engine
}

type invokeRequest struct {
	Inputs  store.JSONMap `json:"inputs"`
	Context store.JSONMap `json:"context"`
}

func (h Handler) Meta(c *gin.Context) {
	httpx.OK(c, gin.H{
		"language":        "go",
		"runtime_version": version.Version,
		"versions":        hub.GetPluginVersions(),
	})
}

func (h Handler) Detail(c *gin.Context) {
	detail, err := hub.GetPluginDetail(c.Param("version"))
	if err != nil {
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"version":        detail.Plugin().Version(),
		"desc":           detail.Plugin().Desc(),
		"inputs":         detail.InputsSchemaJSON(),
		"context_inputs": detail.ContextInputsSchemaJSON(),
		"outputs":        detail.OutputsSchemaJSON(),
		"forms": gin.H{
			"renderform": detail.FormsRenderFormJSON(),
		},
	})
}

func (h Handler) RequireScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.AllowRequest(c.Request, hub.GetOptions().AllowScope) {
			httpx.Error(c, http.StatusForbidden, 40300, "scope is not allowed")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (h Handler) Invoke(c *gin.Context) {
	versionCode := c.Param("version")
	if _, err := hub.GetPluginDetail(versionCode); err != nil {
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}

	var req invokeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	pluginCallbackInfo, _ := finishcallback.Parse(req.Context)
	traceID := uuid.NewString()
	schedule := &store.Schedule{
		TraceID:            traceID,
		PluginVersion:      versionCode,
		State:              constants.StateEmpty,
		InvokeCount:        1,
		Inputs:             req.Inputs,
		ContextInputs:      req.Context,
		ContextData:        store.JSONMap{},
		Outputs:            store.JSONMap{},
		PluginCallbackURL:  pluginCallbackInfo.URL,
		PluginCallbackData: pluginCallbackInfo.Data,
		NextRunAt:          time.Now().UTC(),
		CallerApp:          auth.CallerApp(c.Request),
		Operator:           auth.Operator(c.Request),
		RequestID:          auth.RequestID(c.Request),
		TenantID:           auth.TenantID(c.Request),
	}
	if err := h.store.Create(c.Request.Context(), schedule); err != nil {
		httpx.Error(c, http.StatusInternalServerError, 50000, err.Error())
		return
	}

	reader := runtimeadapter.Reader{Inputs: req.Inputs, ContextInputs: req.Context}
	rt := runtimeadapter.NewExecuteRuntime(c.Request.Context(), h.store, 1)
	logger := h.logger.WithField("trace_id", traceID)
	state, err := executor.Execute(traceID, versionCode, reader, rt, logger)
	if err != nil {
		_ = h.store.MarkFail(c.Request.Context(), traceID, 1, err.Error())
		if saved, getErr := h.store.Get(c.Request.Context(), traceID); getErr == nil {
			h.notifyFinish(c.Request.Context(), saved)
		}
		httpx.OK(c, gin.H{"trace_id": traceID, "state": constants.StateFail})
		return
	}
	if state == constants.StateSuccess {
		_ = h.store.MarkSuccess(c.Request.Context(), traceID, 1)
	}
	saved, err := h.store.Get(c.Request.Context(), traceID)
	if err != nil {
		httpx.Error(c, http.StatusInternalServerError, 50000, err.Error())
		return
	}
	h.notifyFinish(c.Request.Context(), saved)
	httpx.OK(c, gin.H{"trace_id": traceID, "state": saved.State, "outputs": saved.Outputs, "callback_url": saved.CallbackURL})
}

func (h Handler) Schedule(c *gin.Context) {
	schedule, err := h.store.Get(c.Request.Context(), c.Param("trace_id"))
	if err != nil {
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}
	httpx.OK(c, gin.H{
		"trace_id": schedule.TraceID,
		"version":  schedule.PluginVersion,
		"state":    schedule.State,
		"outputs":  schedule.Outputs,
		"error": gin.H{
			"code":    schedule.ErrorCode,
			"message": schedule.ErrorMessage,
		},
	})
}

func (h Handler) Callback(c *gin.Context) {
	token := c.Param("token")
	manager := callback.NewTokenManager(os.Getenv("BK_PLUGIN_CALLBACK_TOKEN_SECRET"))
	traceID, err := manager.Verify(token)
	if err != nil {
		httpx.Error(c, http.StatusUnauthorized, 40100, err.Error())
		return
	}
	var payload store.JSONMap
	if err := c.ShouldBindJSON(&payload); err != nil {
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err := h.store.ReceiveCallback(c.Request.Context(), traceID, callback.Hash(token), payload, time.Now().UTC()); err != nil {
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}
	httpx.OK(c, gin.H{"trace_id": traceID, "state": constants.StateCallback})
}

func (h Handler) notifyFinish(ctx context.Context, schedule *store.Schedule) {
	if schedule == nil || !isFinished(schedule.State) || !hub.GetOptions().EnablePluginCallback || schedule.PluginCallbackURL == "" {
		return
	}
	info := finishcallback.Info{URL: schedule.PluginCallbackURL, Data: schedule.PluginCallbackData}
	if err := finishcallback.NotifyWithRetry(ctx, http.DefaultClient, info); err != nil {
		h.logger.WithError(err).WithField("trace_id", schedule.TraceID).Error("plugin finish callback failed")
	}
}

func isFinished(state constants.State) bool {
	return state == constants.StateSuccess || state == constants.StateFail
}
