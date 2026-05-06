package blueappsadapter

import (
	"context"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/samber/lo"

	"github.com/TencentBlueKing/blueapps-go/pkg/cache/memory"
	"github.com/TencentBlueKing/blueapps-go/pkg/config"
	"github.com/TencentBlueKing/blueapps-go/pkg/i18n"
	"github.com/TencentBlueKing/blueapps-go/pkg/infras/database"
	"github.com/TencentBlueKing/blueapps-go/pkg/infras/redis"
	log "github.com/TencentBlueKing/blueapps-go/pkg/logging"
)

func LoadAndInit(ctx context.Context, cfgFile string) (*config.Config, error) {
	cfg, err := config.Load(ctx, cfgFile)
	if err != nil {
		return nil, errors.Wrap(err, "load blueapps config")
	}
	i18n.InitMsgMap()
	if err := initLoggers(&cfg.Service.Log); err != nil {
		return nil, err
	}
	database.InitDBClient(ctx, cfg.Platform.Addons.Mysql, log.GetLogger("gorm"))
	if cfg.Platform.Addons.Redis != nil {
		redis.InitRedisClient(ctx, cfg.Platform.Addons.Redis)
	}
	memory.InitCache(cfg.Service.MemoryCacheSize)
	return cfg, nil
}

func initLoggers(cfg *config.LogConfig) error {
	if err := os.MkdirAll(cfg.Dir, os.ModePerm); err != nil && !os.IsExist(err) {
		return errors.Wrapf(err, "create log dir %s", cfg.Dir)
	}
	writerName := "file"
	if cfg.ForceToStdout {
		writerName = "stdout"
	}
	if err := initLogger("default", cfg.Level, lo.Ternary(writerName == "stdout", "text", "json"), writerName, filepath.Join(cfg.Dir, "default.log")); err != nil {
		return err
	}
	if err := initLogger("gorm", log.GormLogLevel, "json", "file", filepath.Join(cfg.Dir, "gorm.log")); err != nil {
		return err
	}
	return initLogger("gin", log.GinLogLevel, "json", "file", filepath.Join(cfg.Dir, "gin.log"))
}

func initLogger(name string, level string, handler string, writer string, filename string) error {
	return log.InitLogger(name, &log.Options{
		Level:        level,
		HandlerName:  handler,
		WriterName:   writer,
		WriterConfig: map[string]string{"filename": filename},
	})
}
