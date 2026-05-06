package auth

import (
	"net/http"

	"github.com/TencentBlueKing/bk-plugin-framework-go/hub"
)

const (
	HeaderAppCode     = "X-Bkapi-App-Code"
	HeaderAppCodeAlt  = "X-Bk-App-Code"
	HeaderOperator    = "X-Bkapi-Username"
	HeaderOperatorAlt = "X-Bkapi-User-Name"
	HeaderRequestID   = "X-Bkapi-Request-Id"
	HeaderTenantID    = "X-Bkapi-Tenant-Id"
	HeaderScopeType   = "Bkplugin-Scope-Type"
	HeaderScopeValue  = "Bkplugin-Scope-Value"
)

// AllowRequest follows the Python framework semantics:
// empty allow_scope means allow all; app codes not listed in allow_scope are also allowed.
func AllowRequest(r *http.Request, allowScope hub.AllowScope) bool {
	if len(allowScope) == 0 {
		return true
	}

	rule, ok := allowScope[CallerApp(r)]
	if !ok {
		return true
	}

	return r.Header.Get(HeaderScopeType) == rule.Type && contains(rule.Value, r.Header.Get(HeaderScopeValue))
}

func CallerApp(r *http.Request) string {
	return firstHeader(r, HeaderAppCode, HeaderAppCodeAlt)
}

func Operator(r *http.Request) string {
	return firstHeader(r, HeaderOperator, HeaderOperatorAlt)
}

func RequestID(r *http.Request) string {
	return r.Header.Get(HeaderRequestID)
}

func TenantID(r *http.Request) string {
	return r.Header.Get(HeaderTenantID)
}

func firstHeader(r *http.Request, keys ...string) string {
	for _, key := range keys {
		if value := r.Header.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
