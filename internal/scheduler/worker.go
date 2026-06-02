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
	if len(items) > 0 {
		w.cfg.Logger.WithFields(logrus.Fields{
			"claimed_count": len(items),
			"worker_id":     w.cfg.WorkerID,
		}).Info("plugin scheduler claimed tasks")
	}
	for _, item := range items {
		invokeCount := item.InvokeCount + 1
		logger := w.cfg.Logger.WithField("trace_id", item.TraceID).WithField("plugin_version", item.PluginVersion)
		logger.WithFields(logrus.Fields{
			"state":             item.State,
			"invoke_count":      invokeCount,
			"callback_received": item.CallbackReceivedAt != nil,
		}).Info("[schedule] run task")

		// When running a CALLBACK-state schedule, log the payload the third-party
		// system sent so operators can tell immediately whether result/data fields
		// matched plugin expectations (e.g. "callback result is false" root cause).
		if item.State == constants.StateCallback && item.CallbackReceivedAt != nil {
			logger.WithFields(logrus.Fields{
				"callback_data": item.CallbackData,
			}).Info("[schedule] callback data snapshot")
		}

		logger.Info("[schedule] prepare reader and runtime")
		reader := runtimeadapter.Reader{Inputs: item.Inputs, ContextInputs: item.ContextInputs, CallbackData: item.CallbackData}
		rt := runtimeadapter.NewExecuteRuntime(ctx, w.cfg.Store, invokeCount, logger)

		logger.Info("[schedule] run execute")
		if err := executor.ScheduleWithState(item.TraceID, item.PluginVersion, invokeCount, item.State, reader, rt, logger); err != nil {
			logger.WithError(err).Error("[schedule] plugin execute error")
		}

		updated, err := w.cfg.Store.Get(ctx, item.TraceID)
		if err != nil {
			logger.WithError(err).Error("[schedule] get schedule after run failed")
			continue
		}
		logger.WithFields(logrus.Fields{
			"state":        updated.State,
			"invoke_count": updated.InvokeCount,
			"finished":     updated.FinishedAt != nil,
		}).Info("[schedule] plugin execute schedule done")
		notifyFinish(ctx, logger, updated)
	}
	return nil
}

func notifyFinish(ctx context.Context, logger *logrus.Entry, schedule *store.Schedule) {
	if schedule == nil || !isFinished(schedule.State) || !hub.GetOptions().EnablePluginCallback || schedule.PluginCallbackURL == "" {
		return
	}
	info := finishcallback.Info{URL: schedule.PluginCallbackURL, Data: schedule.PluginCallbackData}
	logger.WithFields(logrus.Fields{
		"state": schedule.State,
	}).Info("plugin finish callback start")
	if err := finishcallback.NotifyWithRetry(ctx, http.DefaultClient, info); err != nil {
		logger.WithError(err).Error("plugin finish callback failed")
		return
	}
	logger.Info("plugin finish callback completed")
}

func isFinished(state constants.State) bool {
	return state == constants.StateSuccess || state == constants.StateFail
}
