package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/auth"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/httpx"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

const pluginAPIPrefix = "/bk_plugin/plugin_api/"
const maxPluginAPIMultipartMemory = 32 << 20

type pluginAPIDispatchRequest struct {
	URL        string        `json:"url"`
	Method     string        `json:"method"`
	Username   string        `json:"username"`
	Data       store.JSONMap `json:"data"`
	DumpedData string        `json:"dumped_data"`
	Files      []pluginAPIDispatchFile
}

type pluginAPIDispatchFile struct {
	FieldName string
	FileName  string
	Content   []byte
}

func (h Handler) PluginAPIDispatch(c *gin.Context) {
	if h.engine == nil {
		httpx.Error(c, http.StatusInternalServerError, 50000, "plugin api router is not initialized")
		return
	}

	req, err := bindPluginAPIDispatchRequest(c)
	if err != nil {
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

	writePluginAPIResponse(c, rec)
}

func bindPluginAPIDispatchRequest(c *gin.Context) (pluginAPIDispatchRequest, error) {
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		return parseMultipartPluginAPIDispatchRequest(c.Request)
	}

	var req pluginAPIDispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return pluginAPIDispatchRequest{}, err
	}
	return req, nil
}

func parseMultipartPluginAPIDispatchRequest(r *http.Request) (pluginAPIDispatchRequest, error) {
	if err := r.ParseMultipartForm(maxPluginAPIMultipartMemory); err != nil {
		return pluginAPIDispatchRequest{}, err
	}

	req := pluginAPIDispatchRequest{
		URL:        r.FormValue("url"),
		Method:     r.FormValue("method"),
		Username:   r.FormValue("username"),
		DumpedData: r.FormValue("dumped_data"),
		Data:       store.JSONMap{},
	}
	if rawData := r.FormValue("data"); rawData != "" {
		if err := json.Unmarshal([]byte(rawData), &req.Data); err != nil {
			return pluginAPIDispatchRequest{}, err
		}
	}
	if r.MultipartForm == nil {
		return req, nil
	}
	for fieldName, headers := range r.MultipartForm.File {
		for _, header := range headers {
			content, err := readMultipartFile(header)
			if err != nil {
				return pluginAPIDispatchRequest{}, err
			}
			req.Files = append(req.Files, pluginAPIDispatchFile{
				FieldName: fieldName,
				FileName:  header.Filename,
				Content:   content,
			})
		}
	}
	return req, nil
}

func readMultipartFile(header *multipart.FileHeader) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
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
	} else if len(r.Files) > 0 {
		payload, contentType, err := r.multipartBody()
		if err != nil {
			return nil, err
		}
		req := httptest.NewRequest(r.Method, target.String(), payload)
		req = req.WithContext(c.Request.Context())
		copyPluginAPIRequestHeaders(req, c.Request.Header)
		req.Header.Set("Content-Type", contentType)
		if r.Username != "" {
			req.Header.Set(auth.HeaderOperator, r.Username)
		}
		return req, nil
	} else {
		payload, err := json.Marshal(r.Data)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(payload)
	}

	req := httptest.NewRequest(r.Method, target.String(), body)
	req = req.WithContext(c.Request.Context())
	copyPluginAPIRequestHeaders(req, c.Request.Header)
	if r.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.Username != "" {
		req.Header.Set(auth.HeaderOperator, r.Username)
	}
	return req, nil
}

func (r pluginAPIDispatchRequest) multipartBody() (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range r.Data {
		if err := writer.WriteField(key, pluginAPIFormFieldValue(value)); err != nil {
			return nil, "", err
		}
	}
	if r.DumpedData != "" {
		if _, ok := r.Data["dumped_data"]; !ok {
			if err := writer.WriteField("dumped_data", r.DumpedData); err != nil {
				return nil, "", err
			}
		}
	}
	for _, file := range r.Files {
		part, err := writer.CreateFormFile(file.FieldName, file.FileName)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(file.Content); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &body, writer.FormDataContentType(), nil
}

func pluginAPIFormFieldValue(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case map[string]interface{}, []interface{}, store.JSONMap:
		raw, err := json.Marshal(v)
		if err == nil {
			return string(raw)
		}
	}
	return fmt.Sprint(value)
}

func copyPluginAPIRequestHeaders(req *http.Request, headers http.Header) {
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
}

func writePluginAPIResponse(c *gin.Context, rec *httptest.ResponseRecorder) {
	for key, values := range rec.Header() {
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	c.Status(rec.Code)
	if rec.Body.Len() == 0 {
		return
	}
	_, _ = c.Writer.Write(rec.Body.Bytes())
}
