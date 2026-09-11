package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewID returns a random identifier with a short readable prefix.
func NewID(prefix string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Errorf("crypto/rand unavailable: %w", err))
	}
	return prefix + "_" + hex.EncodeToString(buf[:])
}

// NewAdminToken generates a high-entropy admin token. The plaintext is
// returned once to the caller; only HashToken output may be persisted.
func NewAdminToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate admin token: %w", err)
	}
	return "csp_" + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// HashToken derives the stored SHA-256 digest of an admin token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenMatches compares a presented token with a stored hash in constant time.
func TokenMatches(token, storedHash string) bool {
	if token == "" || storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(storedHash)) == 1
}
