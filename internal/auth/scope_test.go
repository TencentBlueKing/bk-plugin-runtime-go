package auth

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
)

func TestAllowRequestScopeRules(t *testing.T) {
	scope := hub.AllowScope{
		"bk_sops": {Type: "project", Value: []string{"1", "2"}},
	}

	req := httptest.NewRequest("POST", "/bk_plugin/invoke/1.0.0", nil)
	require.True(t, AllowRequest(req, scope))

	req.Header.Set(HeaderAppCode, "bk_sops")
	req.Header.Set(HeaderScopeType, "project")
	req.Header.Set(HeaderScopeValue, "1")
	require.True(t, AllowRequest(req, scope))

	req.Header.Set(HeaderScopeValue, "3")
	require.False(t, AllowRequest(req, scope))
}
