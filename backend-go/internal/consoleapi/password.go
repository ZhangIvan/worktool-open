package consoleapi

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

const passwordIterations = 600000
const dummyPassword = "worktool-pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA==$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// VerifyPassword accepts only this explicitly versioned bounded format. Legacy
// Node scrypt and disabled accounts require separate credential migration.
// Callers must bound concurrent invocations; this function performs no DB work.
func VerifyPassword(encoded, password string) bool {
	if len(encoded) > 128 {
		return false
	}
	if len(password) < 1 || len(password) > 1024 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "worktool-pbkdf2-sha256" || parts[1] != "600000" {
		return false
	}
	salt, e := base64.StdEncoding.Strict().DecodeString(parts[2])
	if e != nil || len(salt) != 16 || base64.StdEncoding.EncodeToString(salt) != parts[2] {
		return false
	}
	hash, e := base64.StdEncoding.Strict().DecodeString(parts[3])
	if e != nil || len(hash) != 32 || base64.StdEncoding.EncodeToString(hash) != parts[3] {
		return false
	}
	derived, e := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return e == nil && subtle.ConstantTimeCompare(derived, hash) == 1
}
