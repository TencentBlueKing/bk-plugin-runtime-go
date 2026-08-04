package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
)

const defaultLegacyScheduleMigrationBatchSize = 500

// LegacyScheduleMigrationOptions controls the explicit beego-runtime schedule
// migration. ReferenceTime is used as next_run_at for unfinished poll tasks so
// the new worker can resume them immediately.
type LegacyScheduleMigrationOptions struct {
	BatchSize     int
	ReferenceTime time.Time
}

// LegacyScheduleMigrationReport summarizes one migration command run.
type LegacyScheduleMigrationReport struct {
	LegacyTableFound bool
	Scanned          int64
	Migrated         int64
	Skipped          int64
	Resumable        int64
}

// legacySchedule is the MySQL-backed schedule model used by beego-runtime.
// Explicit column tags are required because the legacy ORM maps TraceID to the
// unusual trace_i_d column and stores context data in context_store.
type legacySchedule struct {
	TraceID       string          `gorm:"column:trace_i_d;primaryKey"`
	PluginVersion string          `gorm:"column:plugin_version"`
	State         constants.State `gorm:"column:state"`
	InvokeCount   int             `gorm:"column:invoke_count"`
	Inputs        string          `gorm:"column:inputs"`
	ContextInputs string          `gorm:"column:context_inputs"`
	ContextStore  string          `gorm:"column:context_store"`
	Outputs       string          `gorm:"column:outputs"`
	Error         string          `gorm:"column:error"`
	CreateAt      time.Time       `gorm:"column:create_at"`
	Finished      bool            `gorm:"column:finished"`
	FinishAt      *time.Time      `gorm:"column:finish_at"`
}

func (legacySchedule) TableName() string {
	return "schedule"
}

// MigrateLegacySchedules copies beego-runtime's singular schedule table into
// bk-plugin-runtime-go's schedules table. The operation is intentionally not
// called by server or worker startup; callers must invoke it explicitly.
//
// The migration is idempotent: trace IDs that already exist in schedules are
// skipped and never overwritten. This is important when a prior migration run
// succeeded and the new worker has already advanced a task.
func MigrateLegacySchedules(
	ctx context.Context,
	db *gorm.DB,
	options LegacyScheduleMigrationOptions,
) (LegacyScheduleMigrationReport, error) {
	report := LegacyScheduleMigrationReport{}
	if db == nil {
		return report, fmt.Errorf("database client is nil")
	}
	batchSize := options.BatchSize
	if batchSize == 0 {
		batchSize = defaultLegacyScheduleMigrationBatchSize
	}
	if batchSize < 0 {
		return report, fmt.Errorf("batch size must be greater than zero")
	}
	referenceTime := options.ReferenceTime.UTC()
	if referenceTime.IsZero() {
		referenceTime = time.Now().UTC()
	}
	if !db.WithContext(ctx).Migrator().HasTable(&legacySchedule{}) {
		return report, nil
	}
	report.LegacyTableFound = true

	if err := db.WithContext(ctx).AutoMigrate(&Schedule{}); err != nil {
		return report, fmt.Errorf("create or update schedules table: %w", err)
	}

	legacyColumns := []string{
		"trace_i_d",
		"plugin_version",
		"state",
		"invoke_count",
		"inputs",
		"context_inputs",
		"context_store",
		"outputs",
		"create_at",
		"finished",
		"finish_at",
	}
	// The error column was added in a later beego-runtime migration. Older
	// deployments can still be migrated without it.
	if db.WithContext(ctx).Migrator().HasColumn(&legacySchedule{}, "Error") {
		legacyColumns = append(legacyColumns, "error")
	}

	lastTraceID := ""
	for {
		var legacyRows []legacySchedule
		query := db.WithContext(ctx).
			Select(legacyColumns).
			Order("trace_i_d ASC").
			Limit(batchSize)
		if lastTraceID != "" {
			query = query.Where("trace_i_d > ?", lastTraceID)
		}
		if err := query.Find(&legacyRows).Error; err != nil {
			return report, fmt.Errorf("read legacy schedule rows after %q: %w", lastTraceID, err)
		}
		if len(legacyRows) == 0 {
			break
		}

		targetRows := make([]Schedule, 0, len(legacyRows))
		for i := range legacyRows {
			target, resumable, err := convertLegacySchedule(legacyRows[i], referenceTime)
			if err != nil {
				return report, err
			}
			if resumable {
				report.Resumable++
			}
			targetRows = append(targetRows, target)
		}

		var inserted int64
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "trace_id"}},
				DoNothing: true,
			}).CreateInBatches(&targetRows, batchSize)
			inserted = result.RowsAffected
			return result.Error
		}); err != nil {
			return report, fmt.Errorf("write schedules batch after %q: %w", lastTraceID, err)
		}

		batchCount := int64(len(legacyRows))
		report.Scanned += batchCount
		report.Migrated += inserted
		report.Skipped += batchCount - inserted
		lastTraceID = legacyRows[len(legacyRows)-1].TraceID
	}

	return report, nil
}

func convertLegacySchedule(row legacySchedule, referenceTime time.Time) (Schedule, bool, error) {
	if row.TraceID == "" {
		return Schedule{}, false, fmt.Errorf("legacy schedule has empty trace_i_d")
	}
	inputs, err := decodeLegacyJSON(row.TraceID, "inputs", row.Inputs)
	if err != nil {
		return Schedule{}, false, err
	}
	contextInputs, err := decodeLegacyJSON(row.TraceID, "context_inputs", row.ContextInputs)
	if err != nil {
		return Schedule{}, false, err
	}
	contextData, err := decodeLegacyJSON(row.TraceID, "context_store", row.ContextStore)
	if err != nil {
		return Schedule{}, false, err
	}
	outputs, err := decodeLegacyJSON(row.TraceID, "outputs", row.Outputs)
	if err != nil {
		return Schedule{}, false, err
	}

	createdAt := row.CreateAt.UTC()
	if createdAt.IsZero() {
		createdAt = referenceTime
	}
	nextRunAt := referenceTime
	resumable := !row.Finished && row.State == constants.StatePoll

	var finishedAt *time.Time
	if row.Finished || row.State == constants.StateSuccess || row.State == constants.StateFail {
		finished := createdAt
		if row.FinishAt != nil && !row.FinishAt.IsZero() {
			finished = row.FinishAt.UTC()
		}
		finishedAt = &finished
		nextRunAt = finished
	}

	errorCode := ""
	if row.Error != "" {
		errorCode = "LEGACY_PLUGIN_EXECUTE_ERROR"
	}
	return Schedule{
		TraceID:       row.TraceID,
		PluginVersion: row.PluginVersion,
		State:         row.State,
		InvokeCount:   row.InvokeCount,
		Inputs:        inputs,
		ContextInputs: contextInputs,
		ContextData:   contextData,
		Outputs:       outputs,
		ErrorCode:     errorCode,
		ErrorMessage:  row.Error,
		NextRunAt:     nextRunAt,
		FinishedAt:    finishedAt,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}, resumable, nil
}

func decodeLegacyJSON(traceID string, field string, raw string) (JSONMap, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return JSONMap{}, nil
	}
	var value JSONMap
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("decode legacy schedule %s field %s: %w", traceID, field, err)
	}
	if value == nil {
		return JSONMap{}, nil
	}
	return value, nil
}
