package runtimeadapter

import (
	"context"
	"time"

	"github.com/TencentBlueKing/bk-plugin-framework-go/runtime"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type ExecuteRuntime struct {
	ctx         context.Context
	store       store.ScheduleStore
	invokeCount int
}

func NewExecuteRuntime(ctx context.Context, scheduleStore store.ScheduleStore, invokeCount int) *ExecuteRuntime {
	return &ExecuteRuntime{ctx: ctx, store: scheduleStore, invokeCount: invokeCount}
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

func (r *ExecuteRuntime) SetFail(traceID string, err error) error {
	return r.store.MarkFail(r.ctx, traceID, r.invokeCount, err.Error())
}

func (r *ExecuteRuntime) SetSuccess(traceID string) error {
	return r.store.MarkSuccess(r.ctx, traceID, r.invokeCount)
}
