package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-framework-go/executor"
	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-framework-go/protocol"
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
	opts := hub.GetOptions()
	httpx.OK(c, protocol.BuildMeta(protocol.MetaOptions{
		Code:           pluginCode(),
		Language:       "go",
		RuntimeVersion: version.Version,
		AllowScope:     opts.AllowScope,
	}))
}

func (h Handler) Detail(c *gin.Context) {
	data, err := protocol.BuildDetail(c.Param("version"), protocol.DetailOptions{
		EnablePluginCallback: hub.GetOptions().EnablePluginCallback,
	})
	if err != nil {
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}
	httpx.OK(c, data)
}

func pluginCode() string {
	for _, key := range []string{"BKPAAS_APP_ID", "APP_CODE", "BK_APP_CODE"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func (h Handler) RequireScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.AllowRequest(c.Request, hub.GetOptions().AllowScope) {
			h.logger.WithFields(requestLogFields(c.Request)).Warn("plugin request scope denied")
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
		h.logger.WithFields(logrus.Fields{"plugin_version": versionCode}).WithError(err).Warn("plugin invoke version not found")
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}

	var req invokeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.WithFields(logrus.Fields{"plugin_version": versionCode}).WithError(err).Warn("plugin invoke request invalid")
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	pluginCallbackInfo, _ := finishcallback.Parse(req.Context)
	traceID := uuid.NewString()
	requestBase := requestBaseURL(c.Request)
	logger := h.logger.WithFields(requestLogFields(c.Request)).WithFields(logrus.Fields{
		"trace_id":                traceID,
		"plugin_version":          versionCode,
		"request_base_url":        requestBase,
		"finish_callback_enabled": pluginCallbackInfo.URL != "",
	})
	logger.Info("plugin invoke request accepted")
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
		logger.WithError(err).Error("plugin invoke schedule create failed")
		httpx.Error(c, http.StatusInternalServerError, 50000, err.Error())
		return
	}

	reader := runtimeadapter.Reader{Inputs: req.Inputs, ContextInputs: req.Context}
	rt := runtimeadapter.NewExecuteRuntimeWithCallbackBaseURL(c.Request.Context(), h.store, 1, requestBase)
	state, err := executor.Execute(traceID, versionCode, reader, rt, logger)
	if err != nil {
		logger.WithError(err).WithField("state", constants.StateFail).Error("plugin invoke execute failed")
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
		logger.WithError(err).Error("plugin invoke get schedule failed")
		httpx.Error(c, http.StatusInternalServerError, 50000, err.Error())
		return
	}
	h.notifyFinish(c.Request.Context(), saved)
	logger.WithFields(logrus.Fields{
		"state":             saved.State,
		"invoke_count":      saved.InvokeCount,
		"callback_url_set":  saved.CallbackURL != "",
		"callback_url_host": callbackURLHost(saved.CallbackURL),
	}).Info("plugin invoke response ready")
	httpx.OK(c, gin.H{"trace_id": traceID, "state": saved.State, "outputs": saved.Outputs, "callback_url": saved.CallbackURL})
}

func (h Handler) Schedule(c *gin.Context) {
	schedule, err := h.store.Get(c.Request.Context(), c.Param("trace_id"))
	if err != nil {
		h.logger.WithFields(requestLogFields(c.Request)).WithField("trace_id", c.Param("trace_id")).WithError(err).Warn("plugin schedule not found")
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}
	h.logger.WithFields(requestLogFields(c.Request)).WithFields(logrus.Fields{
		"trace_id":          schedule.TraceID,
		"plugin_version":    schedule.PluginVersion,
		"state":             schedule.State,
		"invoke_count":      schedule.InvokeCount,
		"callback_received": schedule.CallbackReceivedAt != nil,
		"finished":          schedule.FinishedAt != nil,
	}).Info("plugin schedule queried")
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
	tokenHash := callback.Hash(token)
	logger := h.logger.WithFields(requestLogFields(c.Request)).WithField("callback_token_hash", shortHash(tokenHash))
	logger.Info("plugin callback request received")
	manager := callback.NewTokenManager(os.Getenv("BK_PLUGIN_CALLBACK_TOKEN_SECRET"))
	traceID, err := manager.Verify(token)
	if err != nil {
		logger.WithError(err).Warn("plugin callback token rejected")
		httpx.Error(c, http.StatusUnauthorized, 40100, err.Error())
		return
	}
	logger = logger.WithField("trace_id", traceID)
	var payload store.JSONMap
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.WithError(err).Warn("plugin callback request invalid")
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err := h.store.ReceiveCallback(c.Request.Context(), traceID, tokenHash, payload, time.Now().UTC()); err != nil {
		logger.WithError(err).Warn("plugin callback receive failed")
		httpx.Error(c, http.StatusNotFound, 40404, err.Error())
		return
	}
	logger.Info("plugin callback received")
	httpx.OK(c, gin.H{"trace_id": traceID, "state": constants.StateCallback})
}

func (h Handler) notifyFinish(ctx context.Context, schedule *store.Schedule) {
	if schedule == nil || !isFinished(schedule.State) || !hub.GetOptions().EnablePluginCallback || schedule.PluginCallbackURL == "" {
		return
	}
	info := finishcallback.Info{URL: schedule.PluginCallbackURL, Data: schedule.PluginCallbackData}
	h.logger.WithFields(logrus.Fields{
		"trace_id":     schedule.TraceID,
		"state":        schedule.State,
		"callback_url": callbackURLHost(schedule.PluginCallbackURL),
	}).Info("plugin finish callback start")
	if err := finishcallback.NotifyWithRetry(ctx, http.DefaultClient, info); err != nil {
		h.logger.WithError(err).WithField("trace_id", schedule.TraceID).Error("plugin finish callback failed")
		return
	}
	h.logger.WithField("trace_id", schedule.TraceID).Info("plugin finish callback completed")
}

func isFinished(state constants.State) bool {
	return state == constants.StateSuccess || state == constants.StateFail
}

func requestBaseURL(req *http.Request) string {
	host := firstForwardedValue(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = req.Host
	}
	if host == "" {
		return ""
	}
	scheme := firstForwardedValue(req.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if req.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + host
}

func firstForwardedValue(value string) string {
	if idx := strings.Index(value, ","); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func requestLogFields(req *http.Request) logrus.Fields {
	return logrus.Fields{
		"caller_app":  auth.CallerApp(req),
		"operator":    auth.Operator(req),
		"request_id":  auth.RequestID(req),
		"tenant_id":   auth.TenantID(req),
		"method":      req.Method,
		"path":        req.URL.Path,
		"remote_addr": req.RemoteAddr,
	}
}

func callbackURLHost(callbackURL string) string {
	if callbackURL == "" {
		return ""
	}
	parsed, err := url.Parse(callbackURL)
	if err != nil {
		return "<invalid>"
	}
	return parsed.Host
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
