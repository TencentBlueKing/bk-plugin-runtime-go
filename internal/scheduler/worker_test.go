package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
	"github.com/TencentBlueKing/bk-plugin-framework-go/kit"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type pollPlugin struct{}

func (p pollPlugin) Version() string { return "9.9.2" }
func (p pollPlugin) Desc() string    { return "poll plugin" }
func (p pollPlugin) Execute(ctx *kit.Context) error {
	if ctx.InvokeCount() == 1 {
		ctx.WaitPoll(time.Millisecond)
		return nil
	}
	return ctx.WriteOutputs(map[string]interface{}{"done": true, "count": ctx.InvokeCount()})
}

func TestWorkerRunsDuePollTask(t *testing.T) {
	ctx := context.Background()
	hub.MustInstallV2(pollPlugin{}, hub.PluginSpec{Form: []byte(`{}`)})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := store.NewGormStore(db)
	require.NoError(t, s.AutoMigrate(ctx))
	require.NoError(t, s.Create(ctx, &store.Schedule{
		TraceID:       "poll-trace",
		PluginVersion: "9.9.2",
		State:         constants.StatePoll,
		InvokeCount:   1,
		Inputs:        store.JSONMap{},
		ContextInputs: store.JSONMap{},
		ContextData:   store.JSONMap{},
		Outputs:       store.JSONMap{},
		NextRunAt:     time.Now().UTC().Add(-time.Second),
	}))

	worker := NewWorker(Config{
		Store:    s,
		WorkerID: "test-worker",
		Limit:    10,
		LockFor:  time.Minute,
		Logger:   logrus.NewEntry(logrus.StandardLogger()),
	})
	require.NoError(t, worker.RunOnce(ctx))

	got, err := s.Get(ctx, "poll-trace")
	require.NoError(t, err)
	require.Equal(t, constants.StateSuccess, got.State)
	require.Equal(t, store.JSONMap{"done": true, "count": float64(2)}, got.Outputs)
}
