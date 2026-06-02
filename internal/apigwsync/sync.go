// Package apigwsync wraps github.com/TencentBlueKing/bk-apigateway-sdks/manager
// to synchronise the standard plugin runtime resources (callback / invoke /
// plugin_api / openapi / plugin_api_dispatch) to a BlueKing API Gateway.
//
// It mirrors the Python framework's `sync_apigateway_if_changed` command,
// including environment-variable conventions (BKPAAS_BK_PLUGIN_APIGW_NAME,
// BK_APIGW_MAINTAINERS, BK_APIGW_IS_PUBLIC, BK_APIGW_IS_OFFICIAL,
// BK_APIGW_RELEASE_VERSION, BKPAAS_DEFAULT_PREALLOCATED_URLS, ...) and the
// resources.yaml template (with pongo2 `{% if %}` branches for backend
// sub-paths).
package apigwsync

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
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

//go:embed resources.yaml
var resourcesYAML []byte

//go:embed definition.yaml
var defaultDefinitionYAML []byte

// SyncOptions controls a one-shot APIGW sync invocation.
type SyncOptions struct {
	// APIName is the APIGW gateway name. Defaults to env BKPAAS_BK_PLUGIN_APIGW_NAME, then BKPAAS_APP_ID.
	APIName string
	// DefinitionPath, when set and the file exists, fully overrides the
	// runtime-bundled default definition.yaml. Mirrors the Python framework's
	// behaviour where users can drop a definition.yaml at the project root to
	// customise gateway metadata. When unset (or pointing to a missing file),
	// the embedded default template (D2/D3 in the design doc) is used.
	DefinitionPath string
	// ReleaseVersion is the resource version to be created. Defaults to
	// env BK_APIGW_RELEASE_VERSION ("1.0.0") + "+" + stage_name, matching the
	// Python framework's default. Format must satisfy SemVer
	// (MAJOR.MINOR.PATCH[+metadata]).
	ReleaseVersion string
	// ReleaseComment goes with the new resource version. Optional.
	ReleaseComment string
	// SkipRelease skips publishing after creating the resource version.
	SkipRelease bool
	// DeleteUnknownResources tells APIGW to drop resources no longer declared in resources.yaml.
	DeleteUnknownResources bool
	// Logger receives progress lines. Defaults to logrus standard logger.
	Logger logrus.FieldLogger
}

// Sync runs the full APIGW synchronisation workflow.
func Sync(ctx context.Context, opts SyncOptions) error {
	_ = ctx
	apiName := resolveAPIName(opts.APIName)
	if apiName == "" {
		return errors.New("apigw name is empty: set BKPAAS_BK_PLUGIN_APIGW_NAME or BKPAAS_APP_ID")
	}
	logger := loggerOrDefault(opts.Logger).WithField("apigw_name", apiName)

	cfg := buildClientConfig()
	settings := buildSettings(apiName)
	stageName := settings["BK_APIGW_STAGE_NAME"].(string)

	// D4 (PaaS-only deployment): require BKPAAS_DEFAULT_PREALLOCATED_URLS to
	// be present and parseable. Otherwise the rendered stage backend host is
	// empty and the gateway will reject the request with a confusing
	// "host: 该字段不能为空" 4xx. Bail out early with a clear message instead.
	if settings["BK_PLUGIN_APIGW_BACKEND_HOST"] == "" {
		return errors.New(
			"stage backend host is empty: set BKPAAS_DEFAULT_PREALLOCATED_URLS " +
				"(JSON map of {<env>: <full URL>}) so the runtime can derive the " +
				"stage backend; this variable is normally injected by BlueKing PaaS",
		)
	}

	defSource, defLabel := pickDefinitionTemplate(opts.DefinitionPath, loggerOrDefault(opts.Logger))
	rendered, err := renderTemplate(defLabel, defSource, settings)
	if err != nil {
		return errors.Wrapf(err, "render %s", defLabel)
	}
	tmpDef, err := writeTempYAML("bk-plugin-definition-*.yaml", rendered)
	if err != nil {
		return errors.Wrap(err, "stage rendered definition")
	}
	defer os.Remove(tmpDef)

	renderedResources, err := renderTemplate("resources.yaml", resourcesYAML, settings)
	if err != nil {
		return errors.Wrap(err, "render runtime resources.yaml")
	}

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
	logger.WithField("stage", stageName).Info("apigw stages synced")

	if _, err := mgr.SyncResourcesConfig(map[string]interface{}{
		"content": string(renderedResources),
		"delete":  opts.DeleteUnknownResources,
	}); err != nil {
		return errors.Wrap(err, "sync resources")
	}
	logger.WithField("delete_unknown", opts.DeleteUnknownResources).Info("apigw resources synced")

	if _, err := mgr.GrantPermissions(); err != nil {
		if errors.Is(err, manager.ErrNotFound) {
			logger.Info("apigw grant_permissions namespace not declared, skip")
		} else {
			return errors.Wrap(err, "grant permissions")
		}
	} else {
		logger.Info("apigw permissions granted")
	}

	version := resolveReleaseVersion(opts.ReleaseVersion, stageName)
	comment := opts.ReleaseComment
	if comment == "" {
		comment = firstNonEmpty(
			os.Getenv("BK_APIGW_RELEASE_COMMENT"),
			fmt.Sprintf("auto release by bk-plugin-runtime-go(stage=%s)", stageName),
		)
	}
	if _, err := mgr.CreateResourceVersion(version, comment); err != nil {
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

// FetchPublicKey fetches the gateway's RSA public key and writes it to destPath.
func FetchPublicKey(ctx context.Context, apiName string, destPath string, logger logrus.FieldLogger) error {
	_ = ctx
	logger = loggerOrDefault(logger)
	apiName = resolveAPIName(apiName)
	if apiName == "" {
		return errors.New("apigw name is empty: set BKPAAS_BK_PLUGIN_APIGW_NAME or BKPAAS_APP_ID")
	}
	if destPath == "" {
		// Mirrors the Python framework's bin/apigw.pub layout (D6) so that an
		// operator switching between Python and Go runtimes can reuse the same
		// downstream tooling expectations.
		destPath = filepath.Join("bin", "apigw.pub")
	}

	mgr, err := manager.NewDefaultManager(apiName, buildClientConfig())
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

// EmbeddedResourcesYAML returns the runtime-owned resources.yaml content (template form, unrendered).
func EmbeddedResourcesYAML() []byte {
	out := make([]byte, len(resourcesYAML))
	copy(out, resourcesYAML)
	return out
}

// EmbeddedDefinitionYAML returns the runtime-owned default definition.yaml
// template (unrendered). Useful for plugin authors who want to inspect or
// fork the default before customising it.
func EmbeddedDefinitionYAML() []byte {
	out := make([]byte, len(defaultDefinitionYAML))
	copy(out, defaultDefinitionYAML)
	return out
}

// pickDefinitionTemplate returns the bytes that should be rendered as the
// gateway definition. Priority:
//  1. caller-supplied path that resolves to a readable file → use that file
//     (D3=b: existence completely overrides the embedded default).
//  2. otherwise → the runtime-bundled default template.
//
// The returned label is used in error messages so failures attribute to the
// correct source.
func pickDefinitionTemplate(path string, logger logrus.FieldLogger) ([]byte, string) {
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			logger.WithField("definition", path).Info("using user-provided definition.yaml")
			return raw, path
		}
	}
	logger.WithField("definition", "<embedded>").Info("using runtime-bundled default definition.yaml")
	out := make([]byte, len(defaultDefinitionYAML))
	copy(out, defaultDefinitionYAML)
	return out, "embedded definition.yaml"
}

// ----- env / config resolution (mirrors Python framework default.py) -----

// resolveAPIName chooses the APIGW gateway name. Priority:
//  1. explicit `--name` flag / SyncOptions.APIName
//  2. env BKPAAS_BK_PLUGIN_APIGW_NAME (PaaS-injected, dedicated to bk-plugin apps)
//  3. env BK_APIGW_NAME (manual override)
//  4. env BKPAAS_APP_ID (last-resort fallback)
func resolveAPIName(name string) string {
	if name != "" {
		return strings.TrimSpace(name)
	}
	for _, key := range []string{"BKPAAS_BK_PLUGIN_APIGW_NAME", "BK_APIGW_NAME", "BKPAAS_APP_ID"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// buildClientConfig assembles bkapi.ClientConfig from PaaS-injected env vars.
// The Python framework uses BKPAAS_APP_ID/_SECRET, plus BK_APIGW_MANAGER_URL_TMPL
// (with two legacy aliases) for the manager endpoint template.
func buildClientConfig() bkapi.ClientConfig {
	return bkapi.ClientConfig{
		AppCode:   firstNonEmpty(os.Getenv("BKPAAS_APP_ID"), os.Getenv("BK_APP_CODE")),
		AppSecret: firstNonEmpty(os.Getenv("BKPAAS_APP_SECRET"), os.Getenv("BK_APP_SECRET")),
		BkApiUrlTmpl: firstNonEmpty(
			os.Getenv("BK_APIGW_MANAGER_URL_TMPL"),
			os.Getenv("BK_APIGW_MANAGER_URL_TEMPL"),
			os.Getenv("BK_API_URL_TMPL"),
		),
	}
}

// buildSettings produces the pongo2 `settings` map. Field set is intentionally
// aligned with bk_plugin_runtime/config/default.py so that user-supplied
// definition.yaml templates from the Python world keep working unchanged.
func buildSettings(apiName string) pongo2.Context {
	bkpaasEnv := firstNonEmpty(os.Getenv("BKPAAS_ENVIRONMENT"), "dev")
	stageName := os.Getenv("BK_APIGW_STAGE_NAME")
	if stageName == "" {
		if bkpaasEnv == "stag" {
			stageName = "stag"
		} else {
			stageName = "prod"
		}
	}

	maintainers := splitCSV(firstNonEmpty(os.Getenv("BK_APIGW_MAINTAINERS"), "admin"))
	isPublic := strings.EqualFold(firstNonEmpty(os.Getenv("BK_APIGW_IS_PUBLIC"), "true"), "true")
	apiType := 10
	if strings.EqualFold(os.Getenv("BK_APIGW_IS_OFFICIAL"), "true") {
		apiType = 1
	}
	defaultTimeout := atoiDefault(os.Getenv("BK_APIGW_DEFAULT_TIMEOUT"), 60)
	stageBackend := parseStageBackend(bkpaasEnv)

	return pongo2.Context{
		"BK_APIGW_NAME":                       apiName,
		"BK_APP_CODE":                         firstNonEmpty(os.Getenv("BKPAAS_APP_ID"), os.Getenv("BK_APP_CODE")),
		"BK_APP_SECRET":                       firstNonEmpty(os.Getenv("BKPAAS_APP_SECRET"), os.Getenv("BK_APP_SECRET")),
		"BK_APIGW_STAGE_NAME":                 stageName,
		"BK_APIGW_MAINTAINERS":                maintainers,
		"BK_APIGW_IS_PUBLIC":                  isPublic,
		"BK_APIGW_IS_OFFICIAL":                apiType,
		"BK_APIGW_DEFAULT_TIMEOUT":            defaultTimeout,
		"BK_PLUGIN_APIGW_BACKEND_HOST":        stageBackend.host,
		"BK_PLUGIN_APIGW_BACKEND_NETLOC":      stageBackend.netloc,
		"BK_PLUGIN_APIGW_BACKEND_SUB_PATH":    stageBackend.subPath,
		"BK_PLUGIN_APIGW_BACKEND_SCHEME":      stageBackend.scheme,
	}
}

type stageBackend struct {
	host    string // e.g. http://example.com
	netloc  string // e.g. example.com
	subPath string // e.g. bk-plugin-demo (no leading slash)
	scheme  string // http / https
}

// parseStageBackend mirrors the Python config.default.py logic:
//
//	BKPAAS_DEFAULT_PREALLOCATED_URLS (json) → host[bkpaas_environment] → split.
func parseStageBackend(env string) stageBackend {
	raw := os.Getenv("BKPAAS_DEFAULT_PREALLOCATED_URLS")
	if raw == "" {
		return stageBackend{scheme: "http"}
	}
	var allocated map[string]string
	if err := json.Unmarshal([]byte(raw), &allocated); err != nil {
		return stageBackend{scheme: "http"}
	}
	addr := allocated[env]
	if addr == "" {
		return stageBackend{scheme: "http"}
	}
	u, err := url.Parse(addr)
	if err != nil {
		return stageBackend{scheme: "http"}
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return stageBackend{
		host:    fmt.Sprintf("%s://%s", scheme, u.Host),
		netloc:  u.Host,
		subPath: strings.Trim(u.Path, "/"),
		scheme:  scheme,
	}
}

// resolveReleaseVersion produces the SemVer string passed to the APIGW
// `create resource version` API. Priority:
//
//  1. explicit argument (caller / --release-version flag) wins as-is.
//  2. env BK_APIGW_RELEASE_VERSION wins as-is when it already contains
//     build metadata ("X.Y.Z+<anything>") — the operator is taking
//     responsibility for uniqueness (e.g. injecting a git short-sha).
//  3. otherwise use "<base>+<UTC timestamp>" so every redeploy mints a
//     fresh version. Stage is intentionally NOT used as the build metadata
//     here, because PaaS preRelease retries within the same stage would
//     otherwise collide with "版本 1.0.0+prod 已存在" 4xx (40002).
//
// `stage` is unused as of v0.2.4 but kept in the signature so callers don't
// need to drop the parameter; older tests covering the legacy "<base>+<stage>"
// behaviour have been updated alongside this refactor.
func resolveReleaseVersion(explicit, _ string) string {
	if explicit != "" {
		return explicit
	}
	envVersion := os.Getenv("BK_APIGW_RELEASE_VERSION")
	if envVersion == "" {
		envVersion = "1.0.0"
	}
	if strings.Contains(envVersion, "+") {
		// Operator-supplied build metadata; do not append anything.
		return envVersion
	}
	return envVersion + "+" + time.Now().UTC().Format("20060102150405")
}

// ----- low-level helpers -----

func renderTemplate(name string, raw []byte, settings pongo2.Context) ([]byte, error) {
	tpl, err := pongo2.FromBytes(raw)
	if err != nil {
		return nil, errors.Wrapf(err, "parse %s", name)
	}
	out, err := tpl.ExecuteBytes(pongo2.Context{
		"settings": settings,
		"environ":  envSnapshot(),
		"data":     nil,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "execute %s", name)
	}
	return out, nil
}

func envSnapshot() map[string]string {
	envs := os.Environ()
	out := make(map[string]string, len(envs))
	for _, kv := range envs {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
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

func splitCSV(s string) []string {
	out := make([]string, 0)
	for _, item := range strings.Split(s, ",") {
		if v := strings.TrimSpace(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func loggerOrDefault(logger logrus.FieldLogger) logrus.FieldLogger {
	if logger != nil {
		return logger
	}
	return logrus.StandardLogger()
}
