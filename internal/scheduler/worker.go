package scheduler

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-plugin-framework-go/executor"
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
		reader := runtimeadapter.Reader{Inputs: item.Inputs, ContextInputs: item.ContextInputs}
		rt := runtimeadapter.NewExecuteRuntime(ctx, w.cfg.Store)
		logger := w.cfg.Logger.WithField("trace_id", item.TraceID).WithField("plugin_version", item.PluginVersion)
		if err := executor.Schedule(item.TraceID, item.PluginVersion, item.InvokeCount+1, reader, rt, logger); err != nil {
			logger.WithError(err).Error("schedule plugin")
		}
	}
	return nil
}
