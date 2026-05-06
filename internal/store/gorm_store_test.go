package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
)

func newTestStore(t *testing.T) *GormStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Schedule{}))
	return NewGormStore(db)
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

func ptrTime(t time.Time) *time.Time {
	return &t
}
