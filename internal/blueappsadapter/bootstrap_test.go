package blueappsadapter

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TencentBlueKing/blueapps-go/pkg/config"
)

type mysqlPoolConfigRecorder struct {
	maxLifetime time.Duration
	maxIdleTime time.Duration
}

func (r *mysqlPoolConfigRecorder) SetConnMaxLifetime(d time.Duration) {
	r.maxLifetime = d
}

func (r *mysqlPoolConfigRecorder) SetConnMaxIdleTime(d time.Duration) {
	r.maxIdleTime = d
}

func TestConfigureMysqlPool(t *testing.T) {
	recorder := &mysqlPoolConfigRecorder{}

	configureMysqlPool(recorder)

	require.Equal(t, 3*time.Minute, recorder.maxLifetime)
	require.Equal(t, 30*time.Second, recorder.maxIdleTime)
}

func TestPrepareBlueappsEnvAliasesLegacyMysqlEnv(t *testing.T) {
	cleanEnv(t, "MYSQL_HOST", "MYSQL_PORT", "MYSQL_NAME", "MYSQL_USER", "MYSQL_PASSWORD")
	t.Setenv("GCS_MYSQL_HOST", "mysql.service")
	t.Setenv("GCS_MYSQL_PORT", "3306")
	t.Setenv("GCS_MYSQL_NAME", "plugin_db")
	t.Setenv("GCS_MYSQL_USER", "plugin_user")
	t.Setenv("GCS_MYSQL_PASSWORD", "plugin_password")

	require.NoError(t, prepareBlueappsEnv())

	require.Equal(t, "mysql.service", os.Getenv("MYSQL_HOST"))
	require.Equal(t, "3306", os.Getenv("MYSQL_PORT"))
	require.Equal(t, "plugin_db", os.Getenv("MYSQL_NAME"))
	require.Equal(t, "plugin_user", os.Getenv("MYSQL_USER"))
	require.Equal(t, "plugin_password", os.Getenv("MYSQL_PASSWORD"))
}

func TestPrepareBlueappsEnvKeepsExplicitMysqlEnv(t *testing.T) {
	cleanEnv(t, "MYSQL_HOST", "GCS_MYSQL_HOST")
	t.Setenv("MYSQL_HOST", "mysql.new")
	t.Setenv("GCS_MYSQL_HOST", "mysql.legacy")

	require.NoError(t, prepareBlueappsEnv())

	require.Equal(t, "mysql.new", os.Getenv("MYSQL_HOST"))
}

func TestBlueappsConfigLoadsLegacyMysqlEnv(t *testing.T) {
	cleanEnv(t,
		"MYSQL_HOST", "MYSQL_PORT", "MYSQL_NAME", "MYSQL_USER", "MYSQL_PASSWORD",
		"GCS_MYSQL_HOST", "GCS_MYSQL_PORT", "GCS_MYSQL_NAME", "GCS_MYSQL_USER", "GCS_MYSQL_PASSWORD",
	)
	oldConfig := config.G
	t.Cleanup(func() {
		config.G = oldConfig
	})
	t.Setenv("BKPAAS_APP_SECRET", "secret")
	t.Setenv("GCS_MYSQL_HOST", "mysql.service")
	t.Setenv("GCS_MYSQL_PORT", "3306")
	t.Setenv("GCS_MYSQL_NAME", "plugin_db")
	t.Setenv("GCS_MYSQL_USER", "plugin_user")
	t.Setenv("GCS_MYSQL_PASSWORD", "plugin_password")

	require.NoError(t, prepareBlueappsEnv())
	cfg, err := config.Load(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, cfg.Platform.Addons.Mysql)
	require.Equal(t, "mysql.service", cfg.Platform.Addons.Mysql.Host)
	require.Equal(t, 3306, cfg.Platform.Addons.Mysql.Port)
	require.Equal(t, "plugin_db", cfg.Platform.Addons.Mysql.Name)
	require.Equal(t, "plugin_user", cfg.Platform.Addons.Mysql.User)
	require.Equal(t, "plugin_password", cfg.Platform.Addons.Mysql.Password)
}

func TestInitI18nIfPresentSkipsMissingFile(t *testing.T) {
	oldConfig := config.G
	t.Cleanup(func() {
		config.G = oldConfig
	})
	config.G = &config.Config{
		Service: config.ServiceConfig{I18nFileBaseDir: t.TempDir()},
	}

	require.NoError(t, initI18nIfPresent())
}

func cleanEnv(t *testing.T, keys ...string) {
	t.Helper()

	values := make(map[string]string, len(keys))
	exists := make(map[string]bool, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		values[key] = value
		exists[key] = ok
		require.NoError(t, os.Unsetenv(key))
	}

	t.Cleanup(func() {
		for _, key := range keys {
			if exists[key] {
				require.NoError(t, os.Setenv(key, values[key]))
			} else {
				require.NoError(t, os.Unsetenv(key))
			}
		}
	})
}
