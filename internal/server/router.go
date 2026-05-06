package server

import (
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

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

	h := Handler{store: cfg.Store, logger: cfg.Logger}
	group := r.Group("/bk_plugin")
	group.GET("/meta", h.Meta)
	group.GET("/detail/:version", h.Detail)
	group.POST("/invoke/:version", h.Invoke)
	group.GET("/schedule/:trace_id", h.Schedule)
	return r
}
