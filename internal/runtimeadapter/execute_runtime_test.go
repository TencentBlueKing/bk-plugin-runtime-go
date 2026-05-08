package runtimeadapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/callback"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

func TestPrepareCallbackIsReusedBySetCallback(t *testing.T) {
	t.Setenv("BK_PLUGIN_CALLBACK_TOKEN_SECRET", "test-secret")
	t.Setenv("BK_PLUGIN_CALLBACK_BASE_URL", "https://plugin.example.com")
	ctx := context.Background()
	s := newRuntimeAdapterTestStore(t)
	require.NoError(t, s.Create(ctx, &store.Schedule{
		TraceID:       "trace-prepare-callback",
		PluginVersion: "1.0.0",
		State:         constants.StateEmpty,
		InvokeCount:   1,
		NextRunAt:     time.Now().UTC(),
	}))

	rt := NewExecuteRuntime(ctx, s, 1)
	preparation, err := rt.PrepareCallback("trace-prepare-callback", "1.0.0", 1, time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, preparation.ID)
	require.True(t, strings.HasPrefix(preparation.URL, "https://plugin.example.com/bk_plugin/callback/"))

	require.NoError(t, rt.SetCallback("trace-prepare-callback", "1.0.0", 1, time.Hour))
	saved, err := s.Get(ctx, "trace-prepare-callback")
	require.NoError(t, err)
	require.Equal(t, preparation.URL, saved.CallbackURL)

	token := strings.TrimPrefix(preparation.URL, "https://plugin.example.com/bk_plugin/callback/")
	require.NoError(t, s.ReceiveCallback(ctx, "trace-prepare-callback", callback.Hash(token), store.JSONMap{"result": true}, time.Now().UTC()))
}

func newRuntimeAdapterTestStore(t *testing.T) *store.GormStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	s := store.NewGormStore(db)
	require.NoError(t, s.AutoMigrate(context.Background()))
	return s
}
