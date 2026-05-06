package store

import (
	"context"
	"time"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
)

type Schedule struct {
	ID            uint            `gorm:"primaryKey"`
	TraceID       string          `gorm:"size:64;uniqueIndex;not null"`
	PluginVersion string          `gorm:"size:32;index;not null"`
	State         constants.State `gorm:"index;not null"`
	InvokeCount   int             `gorm:"not null"`
	Inputs        JSONMap         `gorm:"type:json"`
	ContextInputs JSONMap         `gorm:"type:json"`
	ContextData   JSONMap         `gorm:"type:json"`
	Outputs       JSONMap         `gorm:"type:json"`
	ErrorCode     string          `gorm:"size:64"`
	ErrorMessage  string          `gorm:"type:text"`
	ErrorDetail   string          `gorm:"type:text"`
	NextRunAt     time.Time       `gorm:"index"`
	LockedBy      string          `gorm:"size:128;index"`
	LockedUntil   *time.Time      `gorm:"index"`
	FinishedAt    *time.Time      `gorm:"index"`
	CallerApp     string          `gorm:"size:64"`
	Operator      string          `gorm:"size:64"`
	RequestID      string          `gorm:"size:128"`
	TenantID       string          `gorm:"size:64"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ScheduleStore interface {
	Create(ctx context.Context, schedule *Schedule) error
	Get(ctx context.Context, traceID string) (*Schedule, error)
	UpdateContextData(ctx context.Context, traceID string, data JSONMap) error
	UpdateOutputs(ctx context.Context, traceID string, data JSONMap) error
	MarkPoll(ctx context.Context, traceID string, invokeCount int, nextRunAt time.Time) error
	MarkSuccess(ctx context.Context, traceID string, invokeCount int) error
	MarkFail(ctx context.Context, traceID string, invokeCount int, message string) error
	ClaimDue(ctx context.Context, now time.Time, workerID string, limit int, lockFor time.Duration) ([]Schedule, error)
}
