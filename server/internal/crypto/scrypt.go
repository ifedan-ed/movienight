package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// Password hashing parameters, as the Python app had them: N=2^15, r=8,
// p=1, a 32-byte key, a 16-byte salt. One hash takes about 0.1 s of CPU and
// 32 MiB of memory (128*N*r bytes). The parameters are written into each
// hash, so they can be raised later and old hashes still verify.
const (
	ScryptN      = 1 << 15
	ScryptR      = 8
	ScryptP      = 1
	scryptKeyLen = 32
	scryptSalt   = 16
)

// Bounds on the parameters a stored hash may ask for. A hash is only ever
// written by this code or the Python, but a corrupted or hand-edited row must
// not make one sign-in attempt allocate gigabytes: anything past these is
// treated as "does not match".
const (
	maxScryptN   = 1 << 20 // 1 GiB at r=8; far above the 2^15 in use
	maxScryptR   = 32
	maxScryptP   = 16
	maxScryptKey = 64
)

// HashPassword returns "scrypt$N$r$p$salt$hash" with standard (padded)
// base64, the exact format the Python wrote. CPU-heavy: callers go through
// passwords.Hasher, which bounds how many run at once.
func HashPassword(password string) (string, error) {
	salt := make([]byte, scryptSalt)
	_, _ = rand.Read(salt)
	key, err := scrypt.Key([]byte(password), salt, ScryptN, ScryptR, ScryptP, scryptKeyLen)
	if err != nil {
		return "", fmt.Errorf("scrypt: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s", ScryptN, ScryptR, ScryptP, b64(salt), b64(key)), nil
}

// CheckPassword reports whether password matches stored. Never errors: a
// malformed or out-of-bounds hash simply does not match (as in the Python).
func CheckPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	n, err1 := strconv.Atoi(parts[1])
	r, err2 := strconv.Atoi(parts[2])
	p, err3 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	if n < 2 || n > maxScryptN || n&(n-1) != 0 || r < 1 || r > maxScryptR || p < 1 || p > maxScryptP {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > maxScryptKey {
		return false
	}
	got, err := scrypt.Key([]byte(password), salt, n, r, p, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
