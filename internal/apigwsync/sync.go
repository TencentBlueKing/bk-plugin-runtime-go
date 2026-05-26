// Package apigwsync wraps github.com/TencentBlueKing/bk-apigateway-sdks/manager
// to synchronise the standard plugin runtime resources (meta/detail/invoke/
// schedule/callback/plugin_api_dispatch/plugin_api) to a BlueKing API Gateway.
//
// It mirrors the Python framework's `sync_apigateway_if_changed` command:
//
//  1. render the user-provided definition.yaml (gateway metadata, stages,
//     grant_permissions, ...) using the SDK's pongo2 context;
//  2. ship the embedded resources.yaml (resource declarations + auth config);
//  3. sync basic info, stages, resources, permissions to APIGW;
//  4. create a new resource version and (optionally) release it.
//
// The embedded resources.yaml lives next to this file so plugin applications
// do not need to maintain their own copy.
package apigwsync

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pongo2 "github.com/flosch/pongo2/v5"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/TencentBlueKing/bk-apigateway-sdks/core/bkapi"
	"github.com/TencentBlueKing/bk-apigateway-sdks/manager"
)

// resourcesYAML is the runtime-owned APIGW resource declaration. It declares
// every endpoint exposed by the plugin runtime (meta/detail/invoke/schedule/
// callback/plugin_api_dispatch/plugin_api) together with the auth config that
// makes the callback path callable by external systems carrying only an app
// code (no user identity).
//
//go:embed resources.yaml
var resourcesYAML []byte

// SyncOptions controls a one-shot APIGW sync invocation.
type SyncOptions struct {
	// APIName is the APIGW gateway name. Defaults to env BK_APIGW_NAME, then BKPAAS_APP_ID.
	APIName string
	// DefinitionPath points to the user-provided definition.yaml (gateway
	// metadata template). Defaults to ./definition.yaml.
	DefinitionPath string
	// ReleaseVersion is the resource version to be created. Defaults to
	// env BK_APIGW_RELEASE_VERSION, then "v"+UTC timestamp.
	ReleaseVersion string
	// ReleaseComment goes with the new resource version. Optional.
	ReleaseComment string
	// SkipRelease skips publishing after creating the resource version.
	SkipRelease bool
	// DeleteUnknownResources tells APIGW to drop resources that are no longer
	// declared in resources.yaml.
	DeleteUnknownResources bool
	// Logger receives progress lines. Defaults to logrus standard logger.
	Logger logrus.FieldLogger
}

// Sync runs the full APIGW synchronisation workflow.
//
// The ctx parameter is reserved for future cancellation support; the
// underlying SDK does not yet honour it but accepting one keeps the public
// surface stable.
func Sync(ctx context.Context, opts SyncOptions) error {
	_ = ctx
	cfg := bkapi.ClientConfig{}
	apiName := resolveAPIName(opts.APIName)
	if apiName == "" {
		return errors.New("apigw name is empty: set BK_APIGW_NAME or BKPAAS_APP_ID")
	}
	logger := loggerOrDefault(opts.Logger).WithField("apigw_name", apiName)

	defPath := opts.DefinitionPath
	if defPath == "" {
		defPath = "definition.yaml"
	}
	rendered, err := renderDefinition(defPath, apiName, &cfg)
	if err != nil {
		return errors.Wrapf(err, "render %s", defPath)
	}

	tmpDef, err := writeTempYAML("bk-plugin-definition-*.yaml", rendered)
	if err != nil {
		return errors.Wrap(err, "stage rendered definition")
	}
	defer os.Remove(tmpDef)

	mgr, err := manager.NewManagerFrom(apiName, cfg, tmpDef)
	if err != nil {
		return errors.Wrap(err, "build apigw manager")
	}

	if _, err := mgr.SyncBasicInfo(); err != nil {
		return errors.Wrap(err, "sync basic info")
	}
	logger.Info("apigw basic info synced")

	if _, err := mgr.SyncStagesConfig(); err != nil {
		return errors.Wrap(err, "sync stages")
	}
	logger.Info("apigw stages synced")

	if _, err := mgr.SyncResourcesConfig(map[string]interface{}{
		"content": string(resourcesYAML),
		"delete":  opts.DeleteUnknownResources,
	}); err != nil {
		return errors.Wrap(err, "sync resources")
	}
	logger.WithField("delete_unknown", opts.DeleteUnknownResources).Info("apigw resources synced")

	// grant_permissions is optional. If the user did not declare any, the SDK
	// returns ErrNotFound (wrapped by pkg/errors). Treat that as a no-op and
	// surface anything else as a hard failure.
	if _, err := mgr.GrantPermissions(); err != nil {
		if errors.Is(err, manager.ErrNotFound) {
			logger.Info("apigw grant_permissions namespace not declared, skip")
		} else {
			return errors.Wrap(err, "grant permissions")
		}
	} else {
		logger.Info("apigw permissions granted")
	}

	version := opts.ReleaseVersion
	if version == "" {
		version = os.Getenv("BK_APIGW_RELEASE_VERSION")
	}
	if version == "" {
		version = fmt.Sprintf("v%s", time.Now().UTC().Format("20060102150405"))
	}
	if _, err := mgr.CreateResourceVersion(version, opts.ReleaseComment); err != nil {
		return errors.Wrapf(err, "create resource version %s", version)
	}
	logger.WithField("version", version).Info("apigw resource version created")

	if opts.SkipRelease {
		logger.Info("skip release as requested")
		return nil
	}
	if _, err := mgr.Release(version); err != nil {
		return errors.Wrapf(err, "release version %s", version)
	}
	logger.WithField("version", version).Info("apigw release completed")
	return nil
}

// FetchPublicKey fetches the gateway's RSA public key and writes it to
// destPath. The directory is created if it does not exist.
func FetchPublicKey(ctx context.Context, apiName string, destPath string, logger logrus.FieldLogger) error {
	_ = ctx
	logger = loggerOrDefault(logger)
	apiName = resolveAPIName(apiName)
	if apiName == "" {
		return errors.New("apigw name is empty: set BK_APIGW_NAME or BKPAAS_APP_ID")
	}
	if destPath == "" {
		destPath = "apigw.pub"
	}

	mgr, err := manager.NewDefaultManager(apiName, bkapi.ClientConfig{})
	if err != nil {
		return errors.Wrap(err, "build apigw manager")
	}
	pub, err := mgr.GetPublicKeyString()
	if err != nil {
		return errors.Wrap(err, "fetch public key")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return errors.Wrap(err, "ensure public key dir")
	}
	if err := os.WriteFile(destPath, []byte(pub), 0o644); err != nil {
		return errors.Wrap(err, "persist public key")
	}
	logger.WithFields(logrus.Fields{
		"apigw_name": apiName,
		"path":       destPath,
		"bytes":      len(pub),
	}).Info("apigw public key fetched")
	return nil
}

// EmbeddedResourcesYAML returns the runtime-owned resources.yaml content. It is
// exported so callers can persist or inspect the resource declarations without
// going through Sync.
func EmbeddedResourcesYAML() []byte {
	out := make([]byte, len(resourcesYAML))
	copy(out, resourcesYAML)
	return out
}

func resolveAPIName(name string) string {
	if name != "" {
		return strings.TrimSpace(name)
	}
	for _, key := range []string{"BK_APIGW_NAME", "BKPAAS_APP_ID", "APP_CODE", "BK_APP_CODE"} {
		if value := os.Getenv(key); value != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func renderDefinition(path string, apiName string, cfg *bkapi.ClientConfig) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "read %s", path)
	}
	tpl, err := pongo2.FromBytes(raw)
	if err != nil {
		return nil, errors.Wrap(err, "parse definition template")
	}
	out, err := tpl.ExecuteBytes(manager.NewDefinitionContext(apiName, cfg).Context(nil))
	if err != nil {
		return nil, errors.Wrap(err, "execute definition template")
	}
	return out, nil
}

func writeTempYAML(pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func loggerOrDefault(logger logrus.FieldLogger) logrus.FieldLogger {
	if logger != nil {
		return logger
	}
	return logrus.StandardLogger()
}
