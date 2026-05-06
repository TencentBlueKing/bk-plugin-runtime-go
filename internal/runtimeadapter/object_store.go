package runtimeadapter

import (
	"context"
	"encoding/json"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type Field string

const (
	FieldContextData Field = "context_data"
	FieldOutputs     Field = "outputs"
)

type ObjectStore struct {
	ctx   context.Context
	store store.ScheduleStore
	field Field
}

func NewObjectStore(ctx context.Context, scheduleStore store.ScheduleStore, field Field) *ObjectStore {
	return &ObjectStore{ctx: ctx, store: scheduleStore, field: field}
}

func (s *ObjectStore) Write(traceID string, v interface{}) error {
	data, err := toJSONMap(v)
	if err != nil {
		return err
	}
	switch s.field {
	case FieldContextData:
		return s.store.UpdateContextData(s.ctx, traceID, data)
	case FieldOutputs:
		return s.store.UpdateOutputs(s.ctx, traceID, data)
	default:
		return nil
	}
}

func (s *ObjectStore) Read(traceID string, v interface{}) error {
	schedule, err := s.store.Get(s.ctx, traceID)
	if err != nil {
		return err
	}
	var data store.JSONMap
	switch s.field {
	case FieldContextData:
		data = schedule.ContextData
	case FieldOutputs:
		data = schedule.Outputs
	default:
		data = store.JSONMap{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func toJSONMap(v interface{}) (store.JSONMap, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return store.ToJSONMap(raw)
}
