package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-framework-go/executor"
	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/httpx"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/runtimeadapter"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/version"
)

type Handler struct {
	store  store.ScheduleStore
	logger *logrus.Entry
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
	traceID := uuid.NewString()
	schedule := &store.Schedule{
		TraceID:       traceID,
		PluginVersion: versionCode,
		State:         constants.StateEmpty,
		InvokeCount:   1,
		Inputs:        req.Inputs,
		ContextInputs: req.Context,
		ContextData:   store.JSONMap{},
		Outputs:       store.JSONMap{},
		NextRunAt:     time.Now().UTC(),
	}
	if err := h.store.Create(c.Request.Context(), schedule); err != nil {
		httpx.Error(c, http.StatusInternalServerError, 50000, err.Error())
		return
	}

	reader := runtimeadapter.Reader{Inputs: req.Inputs, ContextInputs: req.Context}
	rt := runtimeadapter.NewExecuteRuntime(c.Request.Context(), h.store)
	logger := h.logger.WithField("trace_id", traceID)
	state, err := executor.Execute(traceID, versionCode, reader, rt, logger)
	if err != nil {
		_ = h.store.MarkFail(c.Request.Context(), traceID, err.Error())
		httpx.OK(c, gin.H{"trace_id": traceID, "state": constants.StateFail})
		return
	}
	if state == constants.StateSuccess {
		_ = h.store.MarkSuccess(c.Request.Context(), traceID)
	}
	saved, err := h.store.Get(c.Request.Context(), traceID)
	if err != nil {
		httpx.Error(c, http.StatusInternalServerError, 50000, err.Error())
		return
	}
	httpx.OK(c, gin.H{"trace_id": traceID, "state": saved.State, "outputs": saved.Outputs})
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
