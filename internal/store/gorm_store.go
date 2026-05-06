package store

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-framework-go/constants"
)

type GormStore struct {
	db *gorm.DB
}

func NewGormStore(db *gorm.DB) *GormStore {
	return &GormStore{db: db}
}

func (s *GormStore) AutoMigrate(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(&Schedule{})
}

func (s *GormStore) Create(ctx context.Context, schedule *Schedule) error {
	if schedule.ContextData == nil {
		schedule.ContextData = JSONMap{}
	}
	if schedule.Outputs == nil {
		schedule.Outputs = JSONMap{}
	}
	if schedule.Inputs == nil {
		schedule.Inputs = JSONMap{}
	}
	if schedule.ContextInputs == nil {
		schedule.ContextInputs = JSONMap{}
	}
	return s.db.WithContext(ctx).Create(schedule).Error
}

func (s *GormStore) Get(ctx context.Context, traceID string) (*Schedule, error) {
	var schedule Schedule
	if err := s.db.WithContext(ctx).Where("trace_id = ?", traceID).First(&schedule).Error; err != nil {
		return nil, err
	}
	return &schedule, nil
}

func (s *GormStore) UpdateContextData(ctx context.Context, traceID string, data JSONMap) error {
	return s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", traceID).Update("context_data", data).Error
}

func (s *GormStore) UpdateOutputs(ctx context.Context, traceID string, data JSONMap) error {
	return s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", traceID).Update("outputs", data).Error
}

func (s *GormStore) MarkPoll(ctx context.Context, traceID string, invokeCount int, nextRunAt time.Time) error {
	return s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", traceID).Updates(map[string]interface{}{
		"state":        constants.StatePoll,
		"invoke_count": invokeCount,
		"next_run_at":  nextRunAt,
		"locked_by":    "",
		"locked_until": nil,
	}).Error
}

func (s *GormStore) MarkSuccess(ctx context.Context, traceID string, invokeCount int) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", traceID).Updates(map[string]interface{}{
		"state":        constants.StateSuccess,
		"invoke_count": invokeCount,
		"finished_at":  &now,
		"locked_by":    "",
		"locked_until": nil,
	}).Error
}

func (s *GormStore) MarkFail(ctx context.Context, traceID string, invokeCount int, message string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", traceID).Updates(map[string]interface{}{
		"state":         constants.StateFail,
		"invoke_count":  invokeCount,
		"error_code":    "PLUGIN_EXECUTE_ERROR",
		"error_message": message,
		"finished_at":   &now,
		"locked_by":     "",
		"locked_until":  nil,
	}).Error
}

func (s *GormStore) ClaimDue(ctx context.Context, now time.Time, workerID string, limit int, lockFor time.Duration) ([]Schedule, error) {
	var candidates []Schedule
	err := s.db.WithContext(ctx).
		Where("state = ?", constants.StatePoll).
		Where("finished_at IS NULL").
		Where("next_run_at <= ?", now).
		Where("locked_until IS NULL OR locked_until < ?", now).
		Order("next_run_at ASC").
		Limit(limit).
		Find(&candidates).Error
	if err != nil {
		return nil, err
	}

	claimed := make([]Schedule, 0, len(candidates))
	lockUntil := now.Add(lockFor)
	for _, item := range candidates {
		result := s.db.WithContext(ctx).Model(&Schedule{}).
			Where("trace_id = ?", item.TraceID).
			Where("state = ?", constants.StatePoll).
			Where("finished_at IS NULL").
			Where("next_run_at <= ?", now).
			Where("locked_until IS NULL OR locked_until < ?", now).
			Updates(map[string]interface{}{"locked_by": workerID, "locked_until": &lockUntil})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			item.LockedBy = workerID
			item.LockedUntil = &lockUntil
			claimed = append(claimed, item)
		}
	}
	return claimed, nil
}
