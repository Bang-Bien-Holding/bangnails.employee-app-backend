// Package tokenx holds the random-bearer-token/SHA-256-digest pair shared
// by every table that stores a hashed token rather than the raw bearer
// value (password_reset_tokens.token_hash, sessions.token_hash) — the raw
// token only ever leaves Generate inside an emailed link or an HTTP
// response body, never touching the database, so a DB leak (backup,
// replica, etc.) can't be replayed as a valid token.
package tokenx

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// Generate returns a random, hex-encoded bearer token of n random bytes
// before encoding.
func Generate(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Hash returns the hex-encoded SHA-256 digest of a bearer token, as stored
// in and looked up from a *.token_hash column.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
