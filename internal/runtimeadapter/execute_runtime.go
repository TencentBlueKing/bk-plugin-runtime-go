package runtimeadapter

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/TencentBlueKing/bk-plugin-framework-go/runtime"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/callback"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type ExecuteRuntime struct {
	ctx         context.Context
	store       store.ScheduleStore
	invokeCount int
	tokenManager *callback.TokenManager
	callbackBaseURL string
}

func NewExecuteRuntime(ctx context.Context, scheduleStore store.ScheduleStore, invokeCount int) *ExecuteRuntime {
	return &ExecuteRuntime{
		ctx:             ctx,
		store:           scheduleStore,
		invokeCount:     invokeCount,
		tokenManager:    callback.NewTokenManager(os.Getenv("BK_PLUGIN_CALLBACK_TOKEN_SECRET")),
		callbackBaseURL: strings.TrimRight(os.Getenv("BK_PLUGIN_CALLBACK_BASE_URL"), "/"),
	}
}

func (r *ExecuteRuntime) GetOutputsStore() runtime.ObjectStore {
	return NewObjectStore(r.ctx, r.store, FieldOutputs)
}

func (r *ExecuteRuntime) GetContextStore() runtime.ObjectStore {
	return NewObjectStore(r.ctx, r.store, FieldContextData)
}

func (r *ExecuteRuntime) SetPoll(traceID string, version string, invokeCount int, after time.Duration) error {
	return r.store.MarkPoll(r.ctx, traceID, invokeCount, time.Now().UTC().Add(after))
}

func (r *ExecuteRuntime) SetCallback(traceID string, version string, invokeCount int, timeout time.Duration) error {
	token, tokenHash, expiresAt, err := r.tokenManager.Issue(traceID, timeout)
	if err != nil {
		return err
	}
	callbackURL := "/bk_plugin/callback/" + token
	if r.callbackBaseURL != "" {
		callbackURL = r.callbackBaseURL + callbackURL
	}
	return r.store.MarkCallback(r.ctx, traceID, invokeCount, tokenHash, expiresAt, callbackURL)
}

func (r *ExecuteRuntime) SetFail(traceID string, err error) error {
	return r.store.MarkFail(r.ctx, traceID, r.invokeCount, err.Error())
}

func (r *ExecuteRuntime) SetSuccess(traceID string) error {
	return r.store.MarkSuccess(r.ctx, traceID, r.invokeCount)
}
