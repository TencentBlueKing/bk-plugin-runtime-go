package blueappsadapter

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/blueapps-go/pkg/cache/memory"
	"github.com/TencentBlueKing/blueapps-go/pkg/config"
	"github.com/TencentBlueKing/blueapps-go/pkg/i18n"
	"github.com/TencentBlueKing/blueapps-go/pkg/infras/database"
	"github.com/TencentBlueKing/blueapps-go/pkg/infras/redis"
	log "github.com/TencentBlueKing/blueapps-go/pkg/logging"
)

func LoadAndInit(ctx context.Context, cfgFile string) (*config.Config, error) {
	if err := prepareBlueappsEnv(); err != nil {
		return nil, err
	}
	cfg, err := config.Load(ctx, cfgFile)
	if err != nil {
		return nil, errors.Wrap(err, "load blueapps config")
	}
	if err := initI18nIfPresent(); err != nil {
		return nil, err
	}
	if err := initLoggers(&cfg.Service.Log); err != nil {
		return nil, err
	}
	if cfg.Platform.Addons.Mysql == nil {
		return nil, errors.New(
			"mysql addon config is required; expected MYSQL_HOST, MYSQL_PORT, MYSQL_NAME, MYSQL_USER, MYSQL_PASSWORD " +
				"or legacy GCS_MYSQL_* env vars",
		)
	}
	database.InitDBClient(ctx, cfg.Platform.Addons.Mysql, log.GetLogger("gorm"))
	if cfg.Platform.Addons.Redis != nil {
		redis.InitRedisClient(ctx, cfg.Platform.Addons.Redis)
	}
	memory.InitCache(cfg.Service.MemoryCacheSize)
	return cfg, nil
}

func prepareBlueappsEnv() error {
	legacyMysqlEnv := map[string]string{
		"MYSQL_HOST":     "GCS_MYSQL_HOST",
		"MYSQL_PORT":     "GCS_MYSQL_PORT",
		"MYSQL_NAME":     "GCS_MYSQL_NAME",
		"MYSQL_USER":     "GCS_MYSQL_USER",
		"MYSQL_PASSWORD": "GCS_MYSQL_PASSWORD",
	}
	for target, legacy := range legacyMysqlEnv {
		if os.Getenv(target) != "" {
			continue
		}
		if value := os.Getenv(legacy); value != "" {
			if err := os.Setenv(target, value); err != nil {
				return errors.Wrapf(err, "set env %s from %s", target, legacy)
			}
		}
	}
	return nil
}

func initI18nIfPresent() error {
	if _, err := os.Stat(i18n.MsgFilepath()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrap(err, "stat i18n file")
	}
	i18n.InitMsgMap()
	return nil
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
	if err := initLogger("gin", log.GinLogLevel, "json", "file", filepath.Join(cfg.Dir, "gin.log")); err != nil {
		return err
	}
	// Route the global logrus logger (used by bk-plugin-runtime-go handlers / executor)
	// to the same destination as the blueapps "default" slog logger, so that runtime
	// application logs appear in default.log alongside framework boot logs.
	return initLogrus(cfg)
}

// initLogrus configures the global logrus.StandardLogger() to write to the same
// file (or stdout) as the blueapps "default" slog logger, using a JSON format
// that matches the blueapps log-platform field naming convention.
func initLogrus(cfg *config.LogConfig) error {
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)
	logrus.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "time",
			logrus.FieldKeyLevel: "levelname",
			logrus.FieldKeyMsg:   "message",
		},
	})
	if cfg.ForceToStdout {
		logrus.SetOutput(os.Stdout)
		return nil
	}
	f, err := os.OpenFile(
		filepath.Join(cfg.Dir, "default.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return errors.Wrapf(err, "open logrus output file")
	}
	logrus.SetOutput(f)
	return nil
}

func initLogger(name string, level string, handler string, writer string, filename string) error {
	return log.InitLogger(name, &log.Options{
		Level:        level,
		HandlerName:  handler,
		WriterName:   writer,
		WriterConfig: map[string]string{"filename": filename},
	})
}
