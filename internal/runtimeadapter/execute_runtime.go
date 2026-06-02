package runtimeadapter

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/TencentBlueKing/bk-plugin-framework-go/runtime"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/callback"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
	"github.com/sirupsen/logrus"
)

type ExecuteRuntime struct {
	ctx              context.Context
	store            store.ScheduleStore
	invokeCount      int
	tokenManager     *callback.TokenManager
	callbackBaseURL  string
	preparedCallback *preparedCallback
}

type preparedCallback struct {
	preparation runtime.CallbackPreparation
	tokenHash   string
	expiresAt   time.Time
}

func NewExecuteRuntime(ctx context.Context, scheduleStore store.ScheduleStore, invokeCount int) *ExecuteRuntime {
	return NewExecuteRuntimeWithCallbackBaseURL(ctx, scheduleStore, invokeCount, "")
}

func NewExecuteRuntimeWithCallbackBaseURL(ctx context.Context, scheduleStore store.ScheduleStore, invokeCount int, callbackBaseURL string) *ExecuteRuntime {
	baseURL := strings.TrimRight(os.Getenv("BK_PLUGIN_CALLBACK_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(callbackBaseURL, "/")
	}
	// NewTokenManager returns (nil, error) when the secret is empty.
	// We store nil here; PrepareCallback/issueCallback will fail with a clear
	// error if the plugin tries to use callback flow without the env var set.
	tokenManager, _ := callback.NewTokenManager(os.Getenv("BK_PLUGIN_CALLBACK_TOKEN_SECRET"))
	return &ExecuteRuntime{
		ctx:             ctx,
		store:           scheduleStore,
		invokeCount:     invokeCount,
		tokenManager:    tokenManager,
		callbackBaseURL: baseURL,
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

func (r *ExecuteRuntime) PrepareCallback(traceID string, version string, invokeCount int, timeout time.Duration) (runtime.CallbackPreparation, error) {
	prepared, err := r.issueCallback(traceID, timeout)
	if err != nil {
		return runtime.CallbackPreparation{}, err
	}
	r.preparedCallback = prepared
	logrus.WithFields(logrus.Fields{
		"trace_id":                 traceID,
		"plugin_version":           version,
		"invoke_count":             invokeCount,
		"callback_timeout_seconds": int(timeout.Seconds()),
		"callback_url_set":         prepared.preparation.URL != "",
	}).Info("plugin callback prepared")
	return prepared.preparation, nil
}

func (r *ExecuteRuntime) SetCallback(traceID string, version string, invokeCount int, timeout time.Duration) error {
	prepared := r.preparedCallback
	if prepared == nil {
		var err error
		prepared, err = r.issueCallback(traceID, timeout)
		if err != nil {
			return err
		}
	}
	if err := r.store.MarkCallback(r.ctx, traceID, invokeCount, prepared.tokenHash, prepared.expiresAt, prepared.preparation.URL); err != nil {
		return err
	}
	logrus.WithFields(logrus.Fields{
		"trace_id":       traceID,
		"plugin_version": version,
		"invoke_count":   invokeCount,
	}).Info("plugin callback state persisted")
	return nil
}

func (r *ExecuteRuntime) issueCallback(traceID string, timeout time.Duration) (*preparedCallback, error) {
	if r.tokenManager == nil {
		return nil, fmt.Errorf("callback token manager is not available: BK_PLUGIN_CALLBACK_TOKEN_SECRET must be set")
	}
	token, tokenHash, expiresAt, err := r.tokenManager.Issue(traceID, timeout)
	if err != nil {
		return nil, err
	}
	callbackURL := "/bk_plugin/callback/" + token
	if r.callbackBaseURL != "" {
		callbackURL = r.callbackBaseURL + callbackURL
	}
	return &preparedCallback{
		preparation: runtime.CallbackPreparation{
			ID:  tokenHash,
			URL: callbackURL,
		},
		tokenHash: tokenHash,
		expiresAt: expiresAt,
	}, nil
}

func (r *ExecuteRuntime) SetFail(traceID string, err error) error {
	return r.store.MarkFail(r.ctx, traceID, r.invokeCount, err.Error())
}

func (r *ExecuteRuntime) SetSuccess(traceID string) error {
	return r.store.MarkSuccess(r.ctx, traceID, r.invokeCount)
}
