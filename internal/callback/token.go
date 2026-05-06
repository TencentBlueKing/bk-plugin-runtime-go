package callback

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultTTL = 24 * time.Hour

type TokenManager struct {
	secret []byte
	now    func() time.Time
}

func NewTokenManager(secret string) *TokenManager {
	if secret == "" {
		secret = "bk-plugin-runtime-dev-secret"
	}
	return &TokenManager{secret: []byte(secret), now: time.Now}
}

func (m *TokenManager) Issue(traceID string, ttl time.Duration) (token string, hash string, expiresAt time.Time, err error) {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	expiresAt = m.now().UTC().Add(ttl)
	nonce, err := randomNonce()
	if err != nil {
		return "", "", time.Time{}, err
	}
	payload := strings.Join([]string{traceID, strconv.FormatInt(expiresAt.Unix(), 10), nonce}, "|")
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	signature := m.sign(encodedPayload)
	token = encodedPayload + "." + signature
	return token, Hash(token), expiresAt, nil
}

func (m *TokenManager) Verify(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid callback token")
	}
	expected := m.sign(parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return "", fmt.Errorf("invalid callback token signature")
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid callback token payload")
	}
	payloadParts := strings.Split(string(rawPayload), "|")
	if len(payloadParts) != 3 {
		return "", fmt.Errorf("invalid callback token payload")
	}
	expiresUnix, err := strconv.ParseInt(payloadParts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid callback token expiry")
	}
	if !m.now().UTC().Before(time.Unix(expiresUnix, 0)) {
		return "", fmt.Errorf("callback token expired")
	}
	return payloadParts[0], nil
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m *TokenManager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
