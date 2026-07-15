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

func TestMigrateLegacySchedulesMapsRowsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newLegacyMigrationTestDB(t, true)
	createdAt := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	finishedAt := createdAt.Add(time.Minute)
	insertLegacySchedule(t, db, legacySchedule{
		TraceID:       "legacy-poll",
		PluginVersion: "1.0.0",
		State:         constants.StatePoll,
		InvokeCount:   3,
		Inputs:        `{"task_id": 42}`,
		ContextInputs: `{"bk_biz_id": 2}`,
		ContextStore:  `{"job_id": "job-1"}`,
		Outputs:       `{"progress": 30}`,
		CreateAt:      createdAt,
	})
	insertLegacySchedule(t, db, legacySchedule{
		TraceID:       "legacy-failed",
		PluginVersion: "1.0.0",
		State:         constants.StateFail,
		InvokeCount:   2,
		Inputs:        `{}`,
		ContextInputs: `{}`,
		ContextStore:  `{}`,
		Outputs:       `{}`,
		Error:         "legacy failure",
		CreateAt:      createdAt,
		Finished:      true,
		FinishAt:      &finishedAt,
	})

	referenceTime := createdAt.Add(2 * time.Hour)
	report, err := MigrateLegacySchedules(ctx, db, LegacyScheduleMigrationOptions{
		BatchSize:     1,
		ReferenceTime: referenceTime,
	})
	require.NoError(t, err)
	require.Equal(t, LegacyScheduleMigrationReport{
		LegacyTableFound: true,
		Scanned:          2,
		Migrated:         2,
		Skipped:          0,
		Resumable:        1,
	}, report)

	s := NewGormStore(db)
	poll, err := s.Get(ctx, "legacy-poll")
	require.NoError(t, err)
	require.NotZero(t, poll.ID)
	require.Equal(t, constants.StatePoll, poll.State)
	require.Equal(t, 3, poll.InvokeCount)
	require.Equal(t, JSONMap{"task_id": float64(42)}, poll.Inputs)
	require.Equal(t, JSONMap{"bk_biz_id": float64(2)}, poll.ContextInputs)
	require.Equal(t, JSONMap{"job_id": "job-1"}, poll.ContextData)
	require.Equal(t, JSONMap{"progress": float64(30)}, poll.Outputs)
	require.Equal(t, referenceTime, poll.NextRunAt)
	require.Nil(t, poll.FinishedAt)

	failed, err := s.Get(ctx, "legacy-failed")
	require.NoError(t, err)
	require.Equal(t, "LEGACY_PLUGIN_EXECUTE_ERROR", failed.ErrorCode)
	require.Equal(t, "legacy failure", failed.ErrorMessage)
	require.NotNil(t, failed.FinishedAt)
	require.Equal(t, finishedAt, *failed.FinishedAt)

	// Simulate the new runtime advancing a migrated task. A repeated migration
	// must not restore stale state from the legacy table.
	require.NoError(t, db.Model(&Schedule{}).
		Where("trace_id = ?", "legacy-poll").
		Updates(map[string]interface{}{
			"state":        constants.StateSuccess,
			"invoke_count": 4,
			"finished_at":  &referenceTime,
		}).Error)

	report, err = MigrateLegacySchedules(ctx, db, LegacyScheduleMigrationOptions{
		BatchSize:     2,
		ReferenceTime: referenceTime.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), report.Scanned)
	require.Equal(t, int64(0), report.Migrated)
	require.Equal(t, int64(2), report.Skipped)

	poll, err = s.Get(ctx, "legacy-poll")
	require.NoError(t, err)
	require.Equal(t, constants.StateSuccess, poll.State)
	require.Equal(t, 4, poll.InvokeCount)
}

func TestMigrateLegacySchedulesSupportsTableWithoutErrorColumn(t *testing.T) {
	ctx := context.Background()
	db := newLegacyMigrationTestDB(t, false)
	insertLegacySchedule(t, db.Omit("Error"), legacySchedule{
		TraceID:       "legacy-old-schema",
		PluginVersion: "1.0.0",
		State:         constants.StatePoll,
		InvokeCount:   1,
		Inputs:        `{}`,
		ContextInputs: `{}`,
		ContextStore:  `{}`,
		Outputs:       `{}`,
		CreateAt:      time.Now().UTC(),
	})

	report, err := MigrateLegacySchedules(ctx, db, LegacyScheduleMigrationOptions{})
	require.NoError(t, err)
	require.True(t, report.LegacyTableFound)
	require.Equal(t, int64(1), report.Migrated)

	got, err := NewGormStore(db).Get(ctx, "legacy-old-schema")
	require.NoError(t, err)
	require.Empty(t, got.ErrorMessage)
}

func TestMigrateLegacySchedulesRejectsInvalidJSONWithoutPartialBatch(t *testing.T) {
	ctx := context.Background()
	db := newLegacyMigrationTestDB(t, true)
	createdAt := time.Now().UTC()
	insertLegacySchedule(t, db, legacySchedule{
		TraceID:       "legacy-good",
		PluginVersion: "1.0.0",
		State:         constants.StatePoll,
		InvokeCount:   1,
		Inputs:        `{}`,
		ContextInputs: `{}`,
		ContextStore:  `{}`,
		Outputs:       `{}`,
		CreateAt:      createdAt,
	})
	insertLegacySchedule(t, db, legacySchedule{
		TraceID:       "legacy-invalid",
		PluginVersion: "1.0.0",
		State:         constants.StatePoll,
		InvokeCount:   1,
		Inputs:        `{invalid`,
		ContextInputs: `{}`,
		ContextStore:  `{}`,
		Outputs:       `{}`,
		CreateAt:      createdAt,
	})

	_, err := MigrateLegacySchedules(ctx, db, LegacyScheduleMigrationOptions{BatchSize: 10})
	require.ErrorContains(t, err, "legacy-invalid field inputs")

	var count int64
	require.NoError(t, db.Model(&Schedule{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestMigrateLegacySchedulesIsNoopWithoutLegacyTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	report, err := MigrateLegacySchedules(context.Background(), db, LegacyScheduleMigrationOptions{})
	require.NoError(t, err)
	require.Equal(t, LegacyScheduleMigrationReport{}, report)
	require.False(t, db.Migrator().HasTable(&Schedule{}))

	_, err = MigrateLegacySchedules(context.Background(), db, LegacyScheduleMigrationOptions{BatchSize: -1})
	require.ErrorContains(t, err, "batch size must be greater than zero")
}

func newLegacyMigrationTestDB(t *testing.T, withError bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	errorColumn := ""
	if withError {
		errorColumn = "error TEXT NULL,"
	}
	require.NoError(t, db.Exec(`CREATE TABLE schedule (
		trace_i_d TEXT PRIMARY KEY,
		plugin_version TEXT NOT NULL,
		state INTEGER NOT NULL,
		invoke_count INTEGER NOT NULL,
		inputs TEXT NOT NULL,
		context_inputs TEXT NOT NULL,
		context_store TEXT NOT NULL,
		outputs TEXT NOT NULL,
		`+errorColumn+`
		create_at DATETIME NOT NULL,
		finished BOOLEAN NOT NULL,
		finish_at DATETIME NULL
	)`).Error)
	return db
}

func insertLegacySchedule(t *testing.T, db *gorm.DB, row legacySchedule) {
	t.Helper()
	require.NoError(t, db.Create(&row).Error)
}
