package store

import (
	"context"
	"sort"
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
	if schedule.CallbackData == nil {
		schedule.CallbackData = JSONMap{}
	}
	if schedule.PluginCallbackData == nil {
		schedule.PluginCallbackData = JSONMap{}
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

func (s *GormStore) MarkCallback(ctx context.Context, traceID string, invokeCount int, tokenHash string, expiresAt time.Time, callbackURL string) error {
	return s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", traceID).Updates(map[string]interface{}{
		"state":                constants.StateCallback,
		"invoke_count":         invokeCount,
		"callback_token_hash":  tokenHash,
		"callback_expires_at":  &expiresAt,
		"callback_received_at": nil,
		"callback_data":        JSONMap{},
		"callback_url":         callbackURL,
		"locked_by":            "",
		"locked_until":         nil,
	}).Error
}

func (s *GormStore) ReceiveCallback(ctx context.Context, traceID string, tokenHash string, data JSONMap, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&Schedule{}).
		Where("trace_id = ?", traceID).
		Where("callback_token_hash = ?", tokenHash).
		Where("state = ?", constants.StateCallback).
		Where("finished_at IS NULL").
		Where("callback_expires_at IS NULL OR callback_expires_at > ?", now).
		Updates(map[string]interface{}{
			"callback_data":        data,
			"callback_received_at": &now,
			"next_run_at":          now,
			"locked_by":            "",
			"locked_until":         nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
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
	pollCandidates, err := s.findDuePollCandidates(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	callbackCandidates, err := s.findDueCallbackCandidates(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	candidates := mergeDueCandidates(pollCandidates, callbackCandidates, limit)

	claimed := make([]Schedule, 0, len(candidates))
	lockUntil := now.Add(lockFor)
	for _, item := range candidates {
		result := s.claimDueItem(ctx, item, now, workerID, lockUntil)
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

func (s *GormStore) findDuePollCandidates(ctx context.Context, now time.Time, limit int) ([]Schedule, error) {
	var candidates []Schedule
	err := duePollCandidatesQuery(s.db.WithContext(ctx), now, limit).
		Find(&candidates).Error
	return candidates, err
}

func (s *GormStore) findDueCallbackCandidates(ctx context.Context, now time.Time, limit int) ([]Schedule, error) {
	var candidates []Schedule
	err := dueCallbackCandidatesQuery(s.db.WithContext(ctx), now, limit).
		Find(&candidates).Error
	return candidates, err
}

func duePollCandidatesQuery(db *gorm.DB, now time.Time, limit int) *gorm.DB {
	return claimableScheduleScope(db, now).
		Where("state = ?", constants.StatePoll).
		Where("next_run_at <= ?", now).
		Order("next_run_at ASC").
		Limit(limit)
}

func dueCallbackCandidatesQuery(db *gorm.DB, now time.Time, limit int) *gorm.DB {
	return claimableScheduleScope(db, now).
		Where("state = ?", constants.StateCallback).
		Where("callback_received_at IS NOT NULL").
		Order("callback_received_at ASC").
		Limit(limit)
}

func claimableScheduleScope(db *gorm.DB, now time.Time) *gorm.DB {
	return db.
		Where("finished_at IS NULL").
		Where("locked_until IS NULL OR locked_until < ?", now)
}

func (s *GormStore) claimDueItem(ctx context.Context, item Schedule, now time.Time, workerID string, lockUntil time.Time) *gorm.DB {
	query := claimableScheduleScope(s.db.WithContext(ctx).Model(&Schedule{}).Where("trace_id = ?", item.TraceID), now)
	switch item.State {
	case constants.StatePoll:
		query = query.Where("state = ?", constants.StatePoll).Where("next_run_at <= ?", now)
	case constants.StateCallback:
		query = query.Where("state = ?", constants.StateCallback).Where("callback_received_at IS NOT NULL")
	default:
		query = query.Where("1 = 0")
	}
	return query.Updates(map[string]interface{}{"locked_by": workerID, "locked_until": &lockUntil})
}

func mergeDueCandidates(pollCandidates []Schedule, callbackCandidates []Schedule, limit int) []Schedule {
	if limit <= 0 {
		return nil
	}
	candidates := make([]Schedule, 0, len(pollCandidates)+len(callbackCandidates))
	candidates = append(candidates, pollCandidates...)
	candidates = append(candidates, callbackCandidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return dueAt(candidates[i]).Before(dueAt(candidates[j]))
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func dueAt(item Schedule) time.Time {
	if item.State == constants.StateCallback && item.CallbackReceivedAt != nil {
		return *item.CallbackReceivedAt
	}
	return item.NextRunAt
}
