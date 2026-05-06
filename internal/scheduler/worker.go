package scheduler

import (
	"context"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-framework-go/executor"
	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/finishcallback"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/runtimeadapter"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type Config struct {
	Store    store.ScheduleStore
	WorkerID string
	Limit    int
	LockFor  time.Duration
	Interval time.Duration
	Logger   *logrus.Entry
}

type Worker struct {
	cfg Config
}

func NewWorker(cfg Config) *Worker {
	if cfg.Limit == 0 {
		cfg.Limit = 10
	}
	if cfg.LockFor == 0 {
		cfg.LockFor = 5 * time.Minute
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = logrus.NewEntry(logrus.StandardLogger())
	}
	return &Worker{cfg: cfg}
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil {
			w.cfg.Logger.WithError(err).Error("run schedule once")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	now := time.Now().UTC()
	items, err := w.cfg.Store.ClaimDue(ctx, now, w.cfg.WorkerID, w.cfg.Limit, w.cfg.LockFor)
	if err != nil {
		return err
	}
	for _, item := range items {
		invokeCount := item.InvokeCount + 1
		reader := runtimeadapter.Reader{Inputs: item.Inputs, ContextInputs: item.ContextInputs, CallbackData: item.CallbackData}
		rt := runtimeadapter.NewExecuteRuntime(ctx, w.cfg.Store, invokeCount)
		logger := w.cfg.Logger.WithField("trace_id", item.TraceID).WithField("plugin_version", item.PluginVersion)
		if err := executor.ScheduleWithState(item.TraceID, item.PluginVersion, invokeCount, item.State, reader, rt, logger); err != nil {
			logger.WithError(err).Error("schedule plugin")
		}
		updated, err := w.cfg.Store.Get(ctx, item.TraceID)
		if err != nil {
			logger.WithError(err).Error("get schedule after run")
			continue
		}
		notifyFinish(ctx, logger, updated)
	}
	return nil
}

func notifyFinish(ctx context.Context, logger *logrus.Entry, schedule *store.Schedule) {
	if schedule == nil || !isFinished(schedule.State) || !hub.GetOptions().EnablePluginCallback || schedule.PluginCallbackURL == "" {
		return
	}
	info := finishcallback.Info{URL: schedule.PluginCallbackURL, Data: schedule.PluginCallbackData}
	if err := finishcallback.NotifyWithRetry(ctx, http.DefaultClient, info); err != nil {
		logger.WithError(err).Error("plugin finish callback failed")
	}
}

func isFinished(state constants.State) bool {
	return state == constants.StateSuccess || state == constants.StateFail
}
