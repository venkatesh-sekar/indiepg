// Package auth implements the panel's admin authentication: Argon2id password
// hashing and verification (PHC-encoded, self-describing), HMAC-SHA256 signed
// session tokens, and a failure-lockout tracker layered over the local store.
//
// Nothing here talks to Postgres. The admin credential and lockout state live
// in the panel's own SQLite store so authentication keeps working even when the
// managed Postgres is down. No function panics in library paths; callers receive
// typed *core.Error values.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// HashParams holds the Argon2id cost parameters. They are encoded into every
// hash string so a stored hash can always be verified even if defaults change.
type HashParams struct {
	Time    uint32 // number of iterations (argon2 "t")
	Memory  uint32 // memory cost in KiB (argon2 "m")
	Threads uint8  // degree of parallelism (argon2 "p")
	KeyLen  uint32 // length of the derived key in bytes
	SaltLen uint32 // length of the random salt in bytes
}

// DefaultHashParams returns OWASP-recommended Argon2id parameters: 19 MiB of
// memory, 2 iterations, 1 lane, a 32-byte key and a 16-byte salt.
func DefaultHashParams() HashParams {
	return HashParams{
		Time:    2,
		Memory:  19 * 1024, // 19 MiB
		Threads: 1,
		KeyLen:  32,
		SaltLen: 16,
	}
}

// argon2idVersion is the algorithm version emitted/accepted in the hash string.
// It mirrors argon2.Version (0x13 == 19).
const argon2idVersion = argon2.Version

// HashPassword derives an Argon2id key for password using p and returns a
// self-describing PHC-style string of the form:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<base64-salt>$<base64-hash>
//
// The salt is cryptographically random. An empty password is rejected.
func HashPassword(password string, p HashParams) (string, error) {
	if password == "" {
		return "", core.ValidationError("password must not be empty")
	}
	p = normalizeParams(p)

	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", core.InternalError("generate salt").Wrap(err)
	}

	key := argon2.IDKey(stringToBytes(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)

	encoded := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2idVersion,
		p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return encoded, nil
}

// VerifyPassword reports whether password matches encodedHash. The comparison
// of the derived key is constant-time. A malformed encodedHash yields a
// CodeValidation error; a correct-format-but-wrong password returns (false, nil).
func VerifyPassword(password, encodedHash string) (bool, error) {
	params, salt, want, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey(stringToBytes(password), salt, params.Time, params.Memory, params.Threads, params.KeyLen)
	if subtle.ConstantTimeEq(int32(len(got)), int32(len(want))) == 0 {
		return false, nil
	}
	if subtle.ConstantTimeCompare(got, want) == 1 {
		return true, nil
	}
	return false, nil
}

// decodeHash parses a PHC-style argon2id string back into its parameters, salt
// and expected key. It is strict: the algorithm must be argon2id and the
// version must match argon2idVersion.
func decodeHash(encoded string) (HashParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "$argon2id$v=19$m=..,t=..,p=..$salt$hash" splits to:
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", "salt", "hash"]
	if len(parts) != 6 || parts[0] != "" {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: malformed encoding")
	}
	if parts[1] != "argon2id" {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: unsupported algorithm %q", parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: bad version field").Wrap(err)
	}
	if version != argon2idVersion {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: unsupported version %d", version)
	}

	var p HashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: bad parameter field").Wrap(err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: bad salt encoding").Wrap(err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: bad key encoding").Wrap(err)
	}
	if len(salt) == 0 || len(key) == 0 {
		return HashParams{}, nil, nil, core.ValidationError("invalid argon2 hash: empty salt or key")
	}

	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(key))
	return p, salt, key, nil
}

// normalizeParams replaces any zero/invalid field with its default so a
// zero-value HashParams still produces a valid, secure hash.
func normalizeParams(p HashParams) HashParams {
	d := DefaultHashParams()
	if p.Time == 0 {
		p.Time = d.Time
	}
	if p.Memory == 0 {
		p.Memory = d.Memory
	}
	if p.Threads == 0 {
		p.Threads = d.Threads
	}
	if p.KeyLen == 0 {
		p.KeyLen = d.KeyLen
	}
	if p.SaltLen == 0 {
		p.SaltLen = d.SaltLen
	}
	return p
}

// stringToBytes converts without an extra allocation guarantee but stays
// simple and correct; argon2 copies internally.
func stringToBytes(s string) []byte { return []byte(s) }
