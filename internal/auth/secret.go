package auth

import (
	"crypto/rand"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// passwordAlphabet is the alphanumeric set used for generated admin/default
// passwords. It deliberately includes upper, lower, and digits for entropy.
const passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// passwordLength is the project-wide default generated password length.
const passwordLength = 48

// sessionSecretLen is the byte length of an HMAC-SHA256 session signing secret.
const sessionSecretLen = 32

// GeneratePassword returns a 48-character cryptographically-random alphanumeric
// password. It draws from crypto/rand over a 62-symbol alphabet, using
// rejection sampling to eliminate the modulo bias that plain byte%62 would
// introduce. It never panics: rather than emit a weak password on the
// (practically impossible) failure of the system CSPRNG, it retries until
// crypto/rand succeeds.
func GeneratePassword() string {
	return randomString(passwordAlphabet, passwordLength)
}

// NewSessionSecret returns 32 cryptographically-random bytes suitable for
// HMAC-SHA256 session signing.
func NewSessionSecret() ([]byte, error) {
	secret := make([]byte, sessionSecretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, core.InternalError("generate session secret").Wrap(err)
	}
	return secret, nil
}

// randomString returns n characters chosen uniformly from alphabet using
// crypto/rand with rejection sampling to avoid modulo bias.
func randomString(alphabet string, n int) string {
	if n <= 0 || len(alphabet) == 0 {
		return ""
	}
	out := make([]byte, n)
	// limit is the largest multiple of len(alphabet) within a byte's range
	// [0,256). It is kept as an int (not a byte) so the divides-evenly case
	// — e.g. a 2-symbol alphabet, where 256 % 2 == 0 — yields 256 rather than
	// overflowing a byte to 0, which would reject every draw and loop forever.
	limit := 256 - (256 % len(alphabet))
	buf := make([]byte, n)
	filled := 0
	for filled < n {
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand.Read never returns an error on supported platforms;
			// if it somehow does, retry rather than emit a weak password.
			continue
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out[filled] = alphabet[int(b)%len(alphabet)]
			filled++
			if filled == n {
				break
			}
		}
	}
	return string(out)
}
