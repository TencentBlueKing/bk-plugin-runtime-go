package apigwsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/TencentBlueKing/bk-apigateway-sdks/core/bkapi"
)

// TestEmbeddedResourcesYAMLDeclaresRuntimeResources is the smoke test for the
// runtime-owned resource manifest. Without these paths the gateway will fall
// back to platform defaults, which is exactly what caused the callback demo to
// hang in production prior to this package landing.
func TestEmbeddedResourcesYAMLDeclaresRuntimeResources(t *testing.T) {
	raw := EmbeddedResourcesYAML()
	if len(raw) == 0 {
		t.Fatalf("embedded resources.yaml must not be empty")
	}

	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("embedded resources.yaml is not valid yaml: %v", err)
	}

	required := []string{
		"/meta",
		"/detail/{version}",
		"/invoke/{version}",
		"/schedule/{trace_id}",
		"/callback/{token}",
		"/plugin_api_dispatch",
		"/bk_plugin/plugin_api/",
	}
	for _, path := range required {
		if _, ok := spec.Paths[path]; !ok {
			t.Errorf("expected path %q to be declared in resources.yaml", path)
		}
	}

	// Callback must be reachable without user identity, otherwise external
	// systems carrying only an X-Bkapi-App-Code will get 401/403.
	cb, ok := spec.Paths["/callback/{token}"]["post"].(map[string]any)
	if !ok {
		t.Fatalf("/callback/{token}.post is missing")
	}
	resource, _ := cb["x-bk-apigateway-resource"].(map[string]any)
	authConfig, _ := resource["authConfig"].(map[string]any)
	if authConfig["userVerifiedRequired"] != false {
		t.Errorf("callback path must declare userVerifiedRequired=false, got %v", authConfig["userVerifiedRequired"])
	}
	if authConfig["appVerifiedRequired"] != true {
		t.Errorf("callback path must declare appVerifiedRequired=true, got %v", authConfig["appVerifiedRequired"])
	}
}

func TestResolveAPIName_FromEnvFallback(t *testing.T) {
	t.Setenv("BK_APIGW_NAME", "")
	t.Setenv("BKPAAS_APP_ID", "fallback-app")
	t.Setenv("BK_APP_CODE", "")
	if got := resolveAPIName(""); got != "fallback-app" {
		t.Fatalf("expected BKPAAS_APP_ID fallback, got %q", got)
	}

	t.Setenv("BK_APIGW_NAME", "primary-name")
	if got := resolveAPIName(""); got != "primary-name" {
		t.Fatalf("expected BK_APIGW_NAME to take priority, got %q", got)
	}

	if got := resolveAPIName("explicit-arg"); got != "explicit-arg" {
		t.Fatalf("explicit argument should win, got %q", got)
	}
}

func TestRenderDefinition_PongoSubstitutions(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "definition.yaml")
	body := `apigateway:
  name: {{ settings.BK_APIGW_NAME }}
  app_code: {{ settings.BK_APP_CODE }}
`
	if err := os.WriteFile(tmpl, []byte(body), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	cfg := bkapi.ClientConfig{AppCode: "demo-app"}
	out, err := renderDefinition(tmpl, "demo-gateway", &cfg)
	if err != nil {
		t.Fatalf("renderDefinition: %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "name: demo-gateway") {
		t.Errorf("expected name substitution, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "app_code: demo-app") {
		t.Errorf("expected app_code substitution, got:\n%s", rendered)
	}
}

func TestSync_RequiresAPIName(t *testing.T) {
	t.Setenv("BK_APIGW_NAME", "")
	t.Setenv("BKPAAS_APP_ID", "")
	t.Setenv("BK_APP_CODE", "")
	t.Setenv("APP_CODE", "")

	err := Sync(context.Background(), SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "apigw name is empty") {
		t.Fatalf("expected empty-name error, got %v", err)
	}
}
