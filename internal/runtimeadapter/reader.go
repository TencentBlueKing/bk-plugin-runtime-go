package runtimeadapter

import (
	"encoding/json"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type Reader struct {
	Inputs        store.JSONMap
	ContextInputs store.JSONMap
}

func (r Reader) ReadInputs(v interface{}) error {
	data, err := json.Marshal(r.Inputs)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (r Reader) ReadContextInputs(v interface{}) error {
	data, err := json.Marshal(r.ContextInputs)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
