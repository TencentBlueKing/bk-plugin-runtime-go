package apigwsync

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// TestEmbeddedResourcesYAMLBackendPathsAvoidGinTrailingSlashRedirect ensures
// every non-subpath resource declares a backend.path that does NOT end in "/".
//
// Background: bk-plugin-runtime-go uses gin, which by default sets
// RedirectTrailingSlash=true. The runtime registers POST routes WITHOUT a
// trailing slash (e.g. POST /bk_plugin/callback/:token). If the gateway's
// backend.path keeps the trailing slash to mirror the Python framework's
// Django-style URLs, gin replies with HTTP 307 Temporary Redirect — and most
// HTTP clients refuse to follow a 307 on POST, so third-party callbacks
// silently fail. This regression test guards against accidentally re-adding
// the trailing slash on callback / invoke / plugin_api_dispatch.
func TestEmbeddedResourcesYAMLBackendPathsAvoidGinTrailingSlashRedirect(t *testing.T) {
	rendered, err := renderTemplate("resources.yaml", EmbeddedResourcesYAML(), buildSettings("demo-gw"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(rendered, &spec); err != nil {
		t.Fatalf("rendered resources.yaml is invalid: %v", err)
	}

	for path, methods := range spec.Paths {
		for method, def := range methods {
			defMap, ok := def.(map[string]any)
			if !ok {
				continue
			}
			res, ok := defMap["x-bk-apigateway-resource"].(map[string]any)
			if !ok {
				continue
			}
			backend, _ := res["backend"].(map[string]any)
			if backend == nil {
				continue
			}
			backendPath, _ := backend["path"].(string)
			matchSubpath, _ := backend["matchSubpath"].(bool)
			if matchSubpath {
				// subpath routes MUST keep a trailing slash so the gateway can
				// concatenate the captured suffix correctly.
				if !strings.HasSuffix(backendPath, "/") {
					t.Errorf("%s %s: subpath backend.path %q must end in '/'", method, path, backendPath)
				}
				continue
			}
			if strings.HasSuffix(backendPath, "/") {
				t.Errorf("%s %s: non-subpath backend.path %q must NOT end in '/' "+
					"(gin would otherwise emit 307 Temporary Redirect)",
					method, path, backendPath)
			}
		}
	}
}

// TestEmbeddedResourcesYAMLDeclaresRuntimeResources is the smoke test for the
// runtime-owned resource manifest. The 5 paths must match the Python
// framework's support-files/resources.yaml exactly; pre-1.0.0 builds shipped
// extra paths (/meta, /detail, /schedule) which broke the Python<->Go
// migration story.
func TestEmbeddedResourcesYAMLDeclaresRuntimeResources(t *testing.T) {
	raw := EmbeddedResourcesYAML()
	if len(raw) == 0 {
		t.Fatalf("embedded resources.yaml must not be empty")
	}

	rendered, err := renderTemplate("resources.yaml", raw, buildSettings("demo-gw"))
	if err != nil {
		t.Fatalf("template render failed: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(rendered, &spec); err != nil {
		t.Fatalf("rendered resources.yaml is not valid yaml: %v", err)
	}

	required := []string{
		"/callback/{token}/",
		"/invoke/{version}/",
		"/bk_plugin/plugin_api/",
		"/bk_plugin/openapi/",
		"/plugin_api_dispatch",
	}
	for _, path := range required {
		if _, ok := spec.Paths[path]; !ok {
			t.Errorf("expected path %q to be declared in resources.yaml", path)
		}
	}
	if len(spec.Paths) != len(required) {
		t.Errorf("expected exactly %d resources, got %d: %v", len(required), len(spec.Paths), keys(spec.Paths))
	}

	// Callback must be reachable without user identity, otherwise external
	// systems carrying only an X-Bkapi-App-Code will get 401/403.
	cb, ok := spec.Paths["/callback/{token}/"]["post"].(map[string]any)
	if !ok {
		t.Fatalf("/callback/{token}/.post is missing")
	}
	resource, _ := cb["x-bk-apigateway-resource"].(map[string]any)
	authConfig, _ := resource["authConfig"].(map[string]any)
	if authConfig["userVerifiedRequired"] != false {
		t.Errorf("callback userVerifiedRequired = %v, want false", authConfig["userVerifiedRequired"])
	}
	if authConfig["appVerifiedRequired"] != true {
		t.Errorf("callback appVerifiedRequired = %v, want true", authConfig["appVerifiedRequired"])
	}
}

func TestResolveAPIName_PriorityOrder(t *testing.T) {
	t.Setenv("BKPAAS_BK_PLUGIN_APIGW_NAME", "")
	t.Setenv("BK_APIGW_NAME", "")
	t.Setenv("BKPAAS_APP_ID", "fallback-app")
	if got := resolveAPIName(""); got != "fallback-app" {
		t.Fatalf("BKPAAS_APP_ID fallback: got %q", got)
	}

	t.Setenv("BK_APIGW_NAME", "manual")
	if got := resolveAPIName(""); got != "manual" {
		t.Fatalf("BK_APIGW_NAME should beat BKPAAS_APP_ID: got %q", got)
	}

	t.Setenv("BKPAAS_BK_PLUGIN_APIGW_NAME", "platform-issued")
	if got := resolveAPIName(""); got != "platform-issued" {
		t.Fatalf("BKPAAS_BK_PLUGIN_APIGW_NAME should be highest priority env: got %q", got)
	}

	if got := resolveAPIName("explicit-arg"); got != "explicit-arg" {
		t.Fatalf("explicit argument should win, got %q", got)
	}
}

func TestBuildClientConfig_PullsCredentialsFromEnv(t *testing.T) {
	t.Setenv("BKPAAS_APP_ID", "demo-app")
	t.Setenv("BKPAAS_APP_SECRET", "shh")
	t.Setenv("BK_API_URL_TMPL", "http://bkapi.example.com/api/{api_name}")
	t.Setenv("BK_APIGW_MANAGER_URL_TMPL", "")
	t.Setenv("BK_APIGW_MANAGER_URL_TEMPL", "")

	cfg := buildClientConfig()
	if cfg.AppCode != "demo-app" || cfg.AppSecret != "shh" {
		t.Errorf("credentials not injected: %+v", cfg)
	}
	if cfg.BkApiUrlTmpl != "http://bkapi.example.com/api/{api_name}" {
		t.Errorf("url tmpl fallback chain broken: %q", cfg.BkApiUrlTmpl)
	}

	t.Setenv("BK_APIGW_MANAGER_URL_TMPL", "http://manager.example.com/api/{api_name}")
	if got := buildClientConfig().BkApiUrlTmpl; got != "http://manager.example.com/api/{api_name}" {
		t.Errorf("BK_APIGW_MANAGER_URL_TMPL should win, got %q", got)
	}
}

func TestBuildSettings_AppliesPythonDefaults(t *testing.T) {
	t.Setenv("BKPAAS_ENVIRONMENT", "stag")
	t.Setenv("BK_APIGW_STAGE_NAME", "")
	t.Setenv("BK_APIGW_MAINTAINERS", "")
	t.Setenv("BK_APIGW_IS_PUBLIC", "")
	t.Setenv("BK_APIGW_IS_OFFICIAL", "")
	t.Setenv("BK_APIGW_DEFAULT_TIMEOUT", "")
	t.Setenv("BKPAAS_DEFAULT_PREALLOCATED_URLS", `{"stag": "http://stag.example.com/sub-app"}`)

	settings := buildSettings("demo-gw")
	if settings["BK_APIGW_STAGE_NAME"] != "stag" {
		t.Errorf("stage name = %v, want stag", settings["BK_APIGW_STAGE_NAME"])
	}
	if got := settings["BK_APIGW_MAINTAINERS"].([]string); len(got) != 1 || got[0] != "admin" {
		t.Errorf("maintainers default = %v, want [admin]", got)
	}
	if settings["BK_APIGW_IS_PUBLIC"] != true {
		t.Errorf("is_public default = %v, want true", settings["BK_APIGW_IS_PUBLIC"])
	}
	if settings["BK_APIGW_IS_OFFICIAL"] != 10 {
		t.Errorf("api_type default = %v, want 10", settings["BK_APIGW_IS_OFFICIAL"])
	}
	if settings["BK_APIGW_DEFAULT_TIMEOUT"] != 60 {
		t.Errorf("timeout default = %v, want 60", settings["BK_APIGW_DEFAULT_TIMEOUT"])
	}
	if settings["BK_PLUGIN_APIGW_BACKEND_HOST"] != "http://stag.example.com" {
		t.Errorf("backend host = %v", settings["BK_PLUGIN_APIGW_BACKEND_HOST"])
	}
	if settings["BK_PLUGIN_APIGW_BACKEND_SUB_PATH"] != "sub-app" {
		t.Errorf("backend sub_path = %v, want sub-app", settings["BK_PLUGIN_APIGW_BACKEND_SUB_PATH"])
	}
}

func TestBuildSettings_ProdStageDefault(t *testing.T) {
	t.Setenv("BKPAAS_ENVIRONMENT", "prod")
	t.Setenv("BK_APIGW_STAGE_NAME", "")
	settings := buildSettings("demo-gw")
	if settings["BK_APIGW_STAGE_NAME"] != "prod" {
		t.Errorf("stage = %v, want prod", settings["BK_APIGW_STAGE_NAME"])
	}
}

func TestResolveReleaseVersion(t *testing.T) {
	timestampRe := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+\+[0-9]{14}$`)

	t.Setenv("BK_APIGW_RELEASE_VERSION", "")
	got := resolveReleaseVersion("", "prod")
	if !timestampRe.MatchString(got) {
		t.Errorf("default version %q does not match base+UTCtimestamp", got)
	}
	if !strings.HasPrefix(got, "1.0.0+") {
		t.Errorf("default base must be 1.0.0, got %q", got)
	}

	// Two consecutive calls within the same second may produce the same
	// timestamp; force a 1s gap to validate the per-deploy uniqueness story.
	time.Sleep(1100 * time.Millisecond)
	got2 := resolveReleaseVersion("", "prod")
	if got == got2 {
		t.Errorf("expected unique version per second, got %q twice", got)
	}

	t.Setenv("BK_APIGW_RELEASE_VERSION", "2.5.1")
	got = resolveReleaseVersion("", "stag")
	if !strings.HasPrefix(got, "2.5.1+") || !timestampRe.MatchString(got) {
		t.Errorf("env base + auto timestamp broken: %q", got)
	}

	// Operator-supplied build metadata must be preserved verbatim so that
	// CI/CD systems can pin a deterministic version (e.g. git short-sha).
	t.Setenv("BK_APIGW_RELEASE_VERSION", "2.5.1+custom")
	if got := resolveReleaseVersion("", "prod"); got != "2.5.1+custom" {
		t.Errorf("user-supplied build metadata should not be re-stamped, got %q", got)
	}
	if got := resolveReleaseVersion("3.0.0+manual", "prod"); got != "3.0.0+manual" {
		t.Errorf("explicit override broken: %q", got)
	}
}

func TestRenderTemplate_DefinitionStyle(t *testing.T) {
	body := []byte(`apigateway:
  name: {{ settings.BK_APIGW_NAME }}
  app_code: {{ settings.BK_APP_CODE }}
  maintainers:
{% for m in settings.BK_APIGW_MAINTAINERS %}    - "{{ m }}"
{% endfor %}`)
	t.Setenv("BKPAAS_APP_ID", "demo-app")
	t.Setenv("BKPAAS_APP_SECRET", "shh")
	t.Setenv("BK_APIGW_MAINTAINERS", "alice, bob")
	settings := buildSettings("demo-gw")
	out, err := renderTemplate("definition.yaml", body, settings)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(out)
	for _, want := range []string{`name: demo-gw`, `app_code: demo-app`, `- "alice"`, `- "bob"`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in:\n%s", want, rendered)
		}
	}
}

func TestSync_RequiresAPIName(t *testing.T) {
	t.Setenv("BKPAAS_BK_PLUGIN_APIGW_NAME", "")
	t.Setenv("BK_APIGW_NAME", "")
	t.Setenv("BKPAAS_APP_ID", "")

	err := Sync(context.Background(), SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "apigw name is empty") {
		t.Fatalf("expected empty-name error, got %v", err)
	}
}

// TestSync_RequiresStageBackendHost ensures we surface a meaningful error when
// PaaS-injected BKPAAS_DEFAULT_PREALLOCATED_URLS is missing instead of letting
// the gateway server reject the request with an opaque "host: 该字段不能为空"
// 4xx (D4=a).
func TestSync_RequiresStageBackendHost(t *testing.T) {
	t.Setenv("BKPAAS_BK_PLUGIN_APIGW_NAME", "demo-gw")
	t.Setenv("BKPAAS_APP_ID", "demo-app")
	t.Setenv("BKPAAS_APP_SECRET", "shh")
	t.Setenv("BKPAAS_DEFAULT_PREALLOCATED_URLS", "")

	err := Sync(context.Background(), SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "stage backend host is empty") {
		t.Fatalf("expected stage backend host error, got %v", err)
	}
}

// TestPickDefinitionTemplate_PrefersUserFile validates D3=b: an existing
// user-supplied definition.yaml fully overrides the embedded default.
func TestPickDefinitionTemplate_PrefersUserFile(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "definition.yaml")
	body := []byte("apigateway:\n  description: from-user\n")
	if err := os.WriteFile(user, body, 0o644); err != nil {
		t.Fatalf("write user template: %v", err)
	}

	got, label := pickDefinitionTemplate(user, silentLogger())
	if string(got) != string(body) {
		t.Errorf("expected user-supplied bytes, got %q", got)
	}
	if label != user {
		t.Errorf("label = %q, want %q", label, user)
	}
}

// TestPickDefinitionTemplate_FallbackEmbedded validates the no-user-file path
// (Q2=A): with the working directory empty, sync should pick the embedded
// template and not error out.
func TestPickDefinitionTemplate_FallbackEmbedded(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "definition.yaml")

	got, label := pickDefinitionTemplate(missing, silentLogger())
	if len(got) == 0 {
		t.Fatalf("expected embedded template bytes")
	}
	if !strings.Contains(label, "embedded") {
		t.Errorf("label should mention 'embedded', got %q", label)
	}
	if string(got) != string(EmbeddedDefinitionYAML()) {
		t.Errorf("fallback bytes do not match EmbeddedDefinitionYAML()")
	}
}

// TestEmbeddedDefinitionYAMLRendersWithRuntimeSettings exercises the embedded
// definition.yaml end-to-end with realistic settings, ensuring the default
// grant_permissions block (bk_sops, D2=a) survives templating intact.
func TestEmbeddedDefinitionYAMLRendersWithRuntimeSettings(t *testing.T) {
	t.Setenv("BKPAAS_ENVIRONMENT", "prod")
	t.Setenv("BKPAAS_DEFAULT_PREALLOCATED_URLS", `{"prod": "http://prod.example.com/sub"}`)
	t.Setenv("BK_APIGW_MAINTAINERS", "alice, bob")

	settings := buildSettings("demo-gw")
	rendered, err := renderTemplate("embedded definition.yaml", EmbeddedDefinitionYAML(), settings)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	var spec struct {
		Apigateway struct {
			Maintainers []string `yaml:"maintainers"`
			IsPublic    bool     `yaml:"is_public"`
			APIType     int      `yaml:"api_type"`
		} `yaml:"apigateway"`
		Stages []struct {
			Name      string `yaml:"name"`
			ProxyHTTP struct {
				Timeout   int `yaml:"timeout"`
				Upstreams struct {
					Hosts []struct {
						Host string `yaml:"host"`
					} `yaml:"hosts"`
				} `yaml:"upstreams"`
			} `yaml:"proxy_http"`
		} `yaml:"stages"`
		GrantPermissions []struct {
			BkAppCode      string `yaml:"bk_app_code"`
			GrantDimension string `yaml:"grant_dimension"`
		} `yaml:"grant_permissions"`
	}
	if err := yaml.Unmarshal(rendered, &spec); err != nil {
		t.Fatalf("rendered embedded definition is invalid yaml: %v\n%s", err, rendered)
	}

	if got := spec.Apigateway.Maintainers; len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("maintainers = %v, want [alice bob]", got)
	}
	if !spec.Apigateway.IsPublic {
		t.Errorf("is_public = false, want true")
	}
	if spec.Apigateway.APIType != 10 {
		t.Errorf("api_type = %d, want 10", spec.Apigateway.APIType)
	}
	if len(spec.Stages) != 1 || spec.Stages[0].Name != "prod" {
		t.Fatalf("stages = %+v, want single prod stage", spec.Stages)
	}
	if got := spec.Stages[0].ProxyHTTP.Upstreams.Hosts; len(got) != 1 || got[0].Host != "http://prod.example.com" {
		t.Errorf("backend host = %+v, want [http://prod.example.com]", got)
	}
	if spec.Stages[0].ProxyHTTP.Timeout != 60 {
		t.Errorf("timeout = %d, want 60", spec.Stages[0].ProxyHTTP.Timeout)
	}
	if len(spec.GrantPermissions) != 1 ||
		spec.GrantPermissions[0].BkAppCode != "bk_sops" ||
		spec.GrantPermissions[0].GrantDimension != "api" {
		t.Errorf("grant_permissions = %+v, want [{bk_sops api}]", spec.GrantPermissions)
	}
}

func silentLogger() logrus.FieldLogger {
	l := logrus.New()
	l.Out = io.Discard
	return l
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
