package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/httpx"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

const pluginAPIPrefix = "/bk_plugin/plugin_api/"

type pluginAPIDispatchRequest struct {
	URL        string        `json:"url"`
	Method     string        `json:"method"`
	Username   string        `json:"username"`
	Data       store.JSONMap `json:"data"`
	DumpedData string        `json:"dumped_data"`
}

func (h Handler) PluginAPIDispatch(c *gin.Context) {
	if h.engine == nil {
		httpx.Error(c, http.StatusInternalServerError, 50000, "plugin api router is not initialized")
		return
	}

	var req pluginAPIDispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err := req.normalize(); err != nil {
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}

	dispatchReq, err := req.toHTTPRequest(c)
	if err != nil {
		httpx.Error(c, http.StatusBadRequest, 40000, err.Error())
		return
	}

	rec := httptest.NewRecorder()
	h.engine.ServeHTTP(rec, dispatchReq)
	if rec.Code >= http.StatusBadRequest {
		httpx.Error(c, rec.Code, rec.Code, rec.Body.String())
		return
	}

	traceID := auth.RequestID(c.Request)
	if traceID == "" {
		traceID = uuid.NewString()
	}
	httpx.OK(c, gin.H{"trace_id": traceID, "data": decodePluginAPIResponse(rec.Body.Bytes())})
}

func (r *pluginAPIDispatchRequest) normalize() error {
	if !strings.HasPrefix(r.URL, pluginAPIPrefix) {
		return fmt.Errorf("url must starts with '%s'", pluginAPIPrefix)
	}
	r.Method = strings.ToUpper(r.Method)
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return fmt.Errorf("not supported method: %s, only support get and post method", r.Method)
	}
	if r.Data == nil {
		r.Data = store.JSONMap{}
	}
	if r.DumpedData == "" {
		return nil
	}

	var dumped store.JSONMap
	if err := json.Unmarshal([]byte(r.DumpedData), &dumped); err != nil {
		return err
	}
	for key, value := range dumped {
		r.Data[key] = value
	}
	return nil
}

func (r pluginAPIDispatchRequest) toHTTPRequest(c *gin.Context) (*http.Request, error) {
	target, err := url.ParseRequestURI(r.URL)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(target.Path, pluginAPIPrefix) {
		return nil, fmt.Errorf("url must starts with '%s'", pluginAPIPrefix)
	}

	var body *bytes.Reader
	if r.Method == http.MethodGet {
		query := target.Query()
		for key, value := range r.Data {
			query.Set(key, fmt.Sprint(value))
		}
		target.RawQuery = query.Encode()
		body = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(r.Data)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(payload)
	}

	req := httptest.NewRequest(r.Method, target.String(), body)
	req = req.WithContext(c.Request.Context())
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if r.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.Username != "" {
		req.Header.Set(auth.HeaderOperator, r.Username)
	}
	return req, nil
}

func decodePluginAPIResponse(raw []byte) interface{} {
	if len(raw) == 0 {
		return store.JSONMap{}
	}

	var data interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	return data
}
