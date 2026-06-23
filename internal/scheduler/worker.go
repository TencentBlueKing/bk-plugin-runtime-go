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
	// Fail callbacks whose third-party notification never arrived so a missing
	// callback does not leave the plugin stuck in WAITING_CALLBACK forever.
	w.expireStuckCallbacks(ctx, now)

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
	for i := range items {
		w.runItem(ctx, items[i])
	}
	return nil
}

func (w *Worker) runItem(ctx context.Context, item store.Schedule) {
	invokeCount := item.InvokeCount + 1
	logger := w.cfg.Logger.WithField("trace_id", item.TraceID).WithField("plugin_version", item.PluginVersion)
	logger.WithFields(logrus.Fields{
		"state":             item.State,
		"invoke_count":      invokeCount,
		"callback_received": item.CallbackReceivedAt != nil,
	}).Info("[schedule] run task")

	// When running a CALLBACK-state schedule, the third-party payload helps
	// tell whether result/data fields matched plugin expectations (e.g.
	// "callback result is false" root cause). It is verbose / potentially
	// sensitive, so keep it at DEBUG.
	if item.State == constants.StateCallback && item.CallbackReceivedAt != nil {
		logger.WithFields(logrus.Fields{
			"callback_data": item.CallbackData,
		}).Debug("[schedule] callback data snapshot")
	}

	logger.Debug("[schedule] prepare reader and runtime")
	reader := runtimeadapter.Reader{Inputs: item.Inputs, ContextInputs: item.ContextInputs, CallbackData: item.CallbackData}
	rt := runtimeadapter.NewExecuteRuntime(ctx, w.cfg.Store, invokeCount, logger)

	// Renew the lock lease while the step runs so a step slower than LockFor is
	// not re-claimed (and re-executed) by another worker once the lease expires.
	stopHeartbeat := w.startHeartbeat(ctx, item.TraceID, logger)
	logger.Debug("[schedule] run execute")
	execErr := executor.ScheduleWithState(item.TraceID, item.PluginVersion, invokeCount, item.State, reader, rt, logger)
	stopHeartbeat()
	if execErr != nil {
		logger.WithError(execErr).Error("[schedule] plugin execute error")
	}

	updated, err := w.cfg.Store.Get(ctx, item.TraceID)
	if err != nil {
		logger.WithError(err).Error("[schedule] get schedule after run failed")
		return
	}
	logger.WithFields(logrus.Fields{
		"state":        updated.State,
		"invoke_count": updated.InvokeCount,
		"finished":     updated.FinishedAt != nil,
	}).Info("[schedule] plugin execute schedule done")
	// Fire finish-callback asynchronously so a slow or unreachable callback URL
	// cannot stall the worker loop and delay other schedules.
	go notifyFinish(context.Background(), logger, updated)
}

// expireStuckCallbacks marks timed-out CALLBACK schedules as failed and fires
// their finish callbacks so callers are told the plugin will not complete.
func (w *Worker) expireStuckCallbacks(ctx context.Context, now time.Time) {
	failed, err := w.cfg.Store.ExpireCallbacks(ctx, now, w.cfg.Limit)
	if err != nil {
		w.cfg.Logger.WithError(err).Error("[schedule] expire stuck callbacks failed")
		return
	}
	for i := range failed {
		item := failed[i]
		logger := w.cfg.Logger.
			WithField("trace_id", item.TraceID).
			WithField("plugin_version", item.PluginVersion)
		logger.WithField("error_code", item.ErrorCode).Warn("[schedule] callback timed out, marked failed")
		go notifyFinish(context.Background(), logger, &item)
	}
}

// startHeartbeat periodically extends the lock lease for traceID while a step
// runs and returns a stop function that blocks until the heartbeat goroutine
// has fully stopped.
func (w *Worker) startHeartbeat(ctx context.Context, traceID string, logger *logrus.Entry) func() {
	interval := w.cfg.LockFor / 3
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				lockUntil := time.Now().UTC().Add(w.cfg.LockFor)
				ok, err := w.cfg.Store.RenewLock(ctx, traceID, w.cfg.WorkerID, lockUntil)
				if err != nil {
					logger.WithError(err).Warn("[schedule] renew lock failed")
					continue
				}
				if !ok {
					logger.Warn("[schedule] lock lost during execution")
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
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
