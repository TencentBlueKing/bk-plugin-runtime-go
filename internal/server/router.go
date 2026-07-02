package server

import (
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-plugin-framework-go/pluginapi"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

type Config struct {
	Store  store.ScheduleStore
	Logger *logrus.Entry
}

func NewRouter(cfg Config) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	if cfg.Logger == nil {
		cfg.Logger = logrus.NewEntry(logrus.StandardLogger())
	}

	h := Handler{store: cfg.Store, logger: cfg.Logger, engine: r}
	group := r.Group("/bk_plugin")
	group.GET("/meta", h.Meta)
	group.GET("/detail/:version", h.Detail)
	group.POST("/invoke/:version", h.RequireScope(), h.Invoke)
	group.GET("/schedule/:trace_id", h.RequireScope(), h.Schedule)
	group.POST("/callback/:token", h.Callback)
	group.POST("/plugin_api_dispatch", h.RequireScope(), h.PluginAPIDispatch)
	pluginAPIGroup := group.Group("/plugin_api", h.RequireScope())
	pluginAPIRouter := ginPluginAPIRouter{
		group:      pluginAPIGroup,
		registered: map[string]struct{}{},
	}
	for _, registrar := range pluginapi.Registrars() {
		registrar(pluginAPIRouter)
	}
	return r
}
