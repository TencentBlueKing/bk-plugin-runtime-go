package callback

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenManagerIssueVerifyAndHash(t *testing.T) {
	manager := NewTokenManager("secret")
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	token, hash, expiresAt, err := manager.Issue("trace-1", time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, Hash(token), hash)
	require.Equal(t, now.Add(time.Hour), expiresAt)

	traceID, err := manager.Verify(token)
	require.NoError(t, err)
	require.Equal(t, "trace-1", traceID)
}

func TestTokenManagerRejectsExpiredToken(t *testing.T) {
	manager := NewTokenManager("secret")
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	token, _, _, err := manager.Issue("trace-1", time.Hour)
	require.NoError(t, err)

	manager.now = func() time.Time { return now.Add(2 * time.Hour) }
	_, err = manager.Verify(token)
	require.EqualError(t, err, "callback token expired")
}
