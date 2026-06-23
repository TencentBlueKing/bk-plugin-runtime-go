package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
)

func newTestStore(t *testing.T) *GormStore {
	t.Helper()
	s, _ := newTestStoreWithDB(t)
	return s
}

func newTestStoreWithDB(t *testing.T) (*GormStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := NewGormStore(db)
	require.NoError(t, s.AutoMigrate(context.Background()))
	return s, db
}

func TestGormStoreAutoMigrateCreatesClaimIndexes(t *testing.T) {
	_, db := newTestStoreWithDB(t)

	require.True(t, db.Migrator().HasIndex(&Schedule{}, "idx_schedules_claim_poll"))
	require.True(t, db.Migrator().HasIndex(&Schedule{}, "idx_schedules_claim_callback"))
}

func TestGormStoreClaimDueCandidateQueriesUseSeparateStateBranches(t *testing.T) {
	_, db := newTestStoreWithDB(t)
	now := time.Date(2026, 5, 8, 7, 44, 48, 0, time.UTC)
	dryRunDB := db.Session(&gorm.Session{DryRun: true})

	var pollCandidates []Schedule
	pollSQL := duePollCandidatesQuery(dryRunDB, now, 10).Find(&pollCandidates).Statement.SQL.String()
	require.Contains(t, pollSQL, "state = ?")
	require.Contains(t, pollSQL, "next_run_at <= ?")
	require.NotContains(t, strings.ToLower(pollSQL), "callback_received_at is not null")
	require.NotContains(t, pollSQL, "OR (state")

	var callbackCandidates []Schedule
	callbackSQL := dueCallbackCandidatesQuery(dryRunDB, now, 10).Find(&callbackCandidates).Statement.SQL.String()
	require.Contains(t, callbackSQL, "state = ?")
	require.Contains(t, strings.ToLower(callbackSQL), "callback_received_at is not null")
	require.NotContains(t, callbackSQL, "next_run_at <= ?")
	require.NotContains(t, callbackSQL, "OR (state")
}

func TestGormStoreCreateAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := s.Create(ctx, &Schedule{
		TraceID:       "trace-1",
		PluginVersion: "1.0.0",
		State:         constants.StatePoll,
		InvokeCount:   1,
		Inputs:        JSONMap{"x": float64(1)},
		ContextInputs: JSONMap{"bk_biz_id": float64(2)},
		NextRunAt:     time.Now().UTC(),
	})
	require.NoError(t, err)

	got, err := s.Get(ctx, "trace-1")
	require.NoError(t, err)
	require.Equal(t, "1.0.0", got.PluginVersion)
	require.Equal(t, constants.StatePoll, got.State)
	require.Equal(t, JSONMap{"x": float64(1)}, got.Inputs)
}

func TestGormStoreClaimDueSkipsLockedRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "due", PluginVersion: "1.0.0", State: constants.StatePoll, InvokeCount: 1, NextRunAt: now.Add(-time.Second)}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "future", PluginVersion: "1.0.0", State: constants.StatePoll, InvokeCount: 1, NextRunAt: now.Add(time.Hour)}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "done", PluginVersion: "1.0.0", State: constants.StateSuccess, InvokeCount: 1, FinishedAt: ptrTime(now)}))

	claimed, err := s.ClaimDue(ctx, now, "worker-a", 5, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "due", claimed[0].TraceID)

	claimedAgain, err := s.ClaimDue(ctx, now, "worker-b", 5, time.Minute)
	require.NoError(t, err)
	require.Empty(t, claimedAgain)
}

func TestGormStoreMarkSuccessPersistsInvokeCount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "success", PluginVersion: "1.0.0", State: constants.StatePoll, InvokeCount: 1, NextRunAt: now.Add(-time.Second)}))
	require.NoError(t, s.MarkSuccess(ctx, "success", 2))

	got, err := s.Get(ctx, "success")
	require.NoError(t, err)
	require.Equal(t, constants.StateSuccess, got.State)
	require.Equal(t, 2, got.InvokeCount)
}

func TestGormStoreReceiveCallbackMakesTaskClaimable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "callback", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1}))
	require.NoError(t, s.MarkCallback(ctx, "callback", 1, "hash", expiresAt, "/bk_plugin/callback/token"))
	require.NoError(t, s.ReceiveCallback(ctx, "callback", "hash", JSONMap{"ok": true}, now))

	claimed, err := s.ClaimDue(ctx, now, "worker-a", 5, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "callback", claimed[0].TraceID)
	require.Equal(t, constants.StateCallback, claimed[0].State)
	require.Equal(t, JSONMap{"ok": true}, claimed[0].CallbackData)
}

func TestGormStoreClaimDueMergesPollAndCallbackByDueTime(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	callbackReceivedAt := now.Add(-20 * time.Second)

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "poll-old", PluginVersion: "1.0.0", State: constants.StatePoll, InvokeCount: 1, NextRunAt: now.Add(-30 * time.Second)}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "callback-due", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1, CallbackReceivedAt: &callbackReceivedAt, NextRunAt: callbackReceivedAt}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "poll-new", PluginVersion: "1.0.0", State: constants.StatePoll, InvokeCount: 1, NextRunAt: now.Add(-5 * time.Second)}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "callback-waiting", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1, NextRunAt: now.Add(-40 * time.Second)}))

	claimed, err := s.ClaimDue(ctx, now, "worker-a", 2, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.Equal(t, "poll-old", claimed[0].TraceID)
	require.Equal(t, "callback-due", claimed[1].TraceID)
}

func TestGormStoreReceiveCallbackIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "cb", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1}))
	require.NoError(t, s.MarkCallback(ctx, "cb", 1, "hash", expiresAt, "/bk_plugin/callback/token"))

	// First callback wins.
	require.NoError(t, s.ReceiveCallback(ctx, "cb", "hash", JSONMap{"n": float64(1)}, now))

	// A worker claims it and holds the lock while executing.
	claimed, err := s.ClaimDue(ctx, now, "worker-a", 5, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// A retried / duplicate callback arriving mid-run must be idempotent: it
	// must not reset the schedule, overwrite callback_data, or clear the lock.
	dupErr := s.ReceiveCallback(ctx, "cb", "hash", JSONMap{"n": float64(2)}, now.Add(time.Second))
	require.ErrorIs(t, dupErr, ErrCallbackAlreadyReceived)

	got, err := s.Get(ctx, "cb")
	require.NoError(t, err)
	require.Equal(t, "worker-a", got.LockedBy)
	require.NotNil(t, got.LockedUntil)
	require.Equal(t, JSONMap{"n": float64(1)}, got.CallbackData)
}

func TestGormStoreReceiveCallbackRejectsUnknownToken(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "cb", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1}))
	require.NoError(t, s.MarkCallback(ctx, "cb", 1, "hash", now.Add(time.Hour), "/cb"))

	err := s.ReceiveCallback(ctx, "cb", "wrong-hash", JSONMap{}, now)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestGormStoreExpireCallbacksFailsTimedOut(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	received := now.Add(-time.Minute)

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "expired", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1, CallbackExpiresAt: &past}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "received", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1, CallbackExpiresAt: &past, CallbackReceivedAt: &received}))
	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "pending", PluginVersion: "1.0.0", State: constants.StateCallback, InvokeCount: 1, CallbackExpiresAt: &future}))

	failed, err := s.ExpireCallbacks(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	require.Equal(t, "expired", failed[0].TraceID)
	require.Equal(t, constants.StateFail, failed[0].State)
	require.Equal(t, "CALLBACK_TIMEOUT", failed[0].ErrorCode)
	require.NotNil(t, failed[0].FinishedAt)

	pending, err := s.Get(ctx, "pending")
	require.NoError(t, err)
	require.Equal(t, constants.StateCallback, pending.State)

	stillReceived, err := s.Get(ctx, "received")
	require.NoError(t, err)
	require.Equal(t, constants.StateCallback, stillReceived.State)
}

func TestGormStoreRenewLock(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()

	require.NoError(t, s.Create(ctx, &Schedule{TraceID: "lk", PluginVersion: "1.0.0", State: constants.StatePoll, InvokeCount: 1, NextRunAt: now.Add(-time.Second)}))
	claimed, err := s.ClaimDue(ctx, now, "worker-a", 5, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	newLease := now.Add(10 * time.Minute)
	ok, err := s.RenewLock(ctx, "lk", "worker-a", newLease)
	require.NoError(t, err)
	require.True(t, ok)
	got, err := s.Get(ctx, "lk")
	require.NoError(t, err)
	require.WithinDuration(t, newLease, *got.LockedUntil, time.Second)

	// A worker that does not own the lock cannot renew it.
	ok, err = s.RenewLock(ctx, "lk", "worker-b", now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, ok)

	// Once finished the lock is gone and cannot be renewed.
	require.NoError(t, s.MarkSuccess(ctx, "lk", 2))
	ok, err = s.RenewLock(ctx, "lk", "worker-a", now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, ok)
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
