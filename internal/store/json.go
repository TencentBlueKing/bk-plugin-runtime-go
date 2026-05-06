package store

import (
	"database/sql/driver"
	"encoding/json"
)

type JSONMap map[string]interface{}

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(m)
}

func (m *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*m = JSONMap{}
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		data = []byte(`{}`)
	}
	return json.Unmarshal(data, m)
}

func ToJSONMap(raw []byte) (JSONMap, error) {
	if len(raw) == 0 {
		return JSONMap{}, nil
	}
	var data JSONMap
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}
