package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TencentBlueKing/bk-plugin-framework-go/pluginapi"
)

type ginPluginAPIRouter struct {
	group      gin.IRouter
	registered map[string]struct{}
}

func (r ginPluginAPIRouter) Handle(method string, path string, handler http.HandlerFunc) {
	normalizedPath := normalizePluginAPIPath(path)
	r.handle(method, normalizedPath, handler)
	if alternatePath := alternatePluginAPIPath(normalizedPath); alternatePath != "" {
		r.handle(method, alternatePath, handler)
	}
}

func (r ginPluginAPIRouter) handle(method string, path string, handler http.HandlerFunc) {
	key := method + " " + path
	if _, ok := r.registered[key]; ok {
		return
	}
	r.registered[key] = struct{}{}
	r.group.Handle(method, path, func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, pluginapi.WithParams(c.Request, ginParams(c.Params)))
	})
}

func (r ginPluginAPIRouter) GET(path string, handler http.HandlerFunc) {
	r.Handle(http.MethodGet, path, handler)
}

func (r ginPluginAPIRouter) POST(path string, handler http.HandlerFunc) {
	r.Handle(http.MethodPost, path, handler)
}

func normalizePluginAPIPath(path string) string {
	if path == "" {
		return "/"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func alternatePluginAPIPath(path string) string {
	if path == "/" {
		return ""
	}
	if strings.HasSuffix(path, "/") {
		return strings.TrimRight(path, "/")
	}
	return path + "/"
}

func ginParams(params gin.Params) map[string]string {
	values := make(map[string]string, len(params))
	for _, param := range params {
		values[param.Key] = param.Value
	}
	return values
}
