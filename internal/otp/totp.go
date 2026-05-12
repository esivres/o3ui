// Package otp implements HOTP (RFC 4226) and TOTP (RFC 6238) — the
// time-based one-time password algorithm used by virtually every
// "Authenticator app" today. Pure Go, no external deps.
package otp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"strings"
	"time"
)

// Algorithm selects the HMAC hash for HOTP.
type Algorithm int

const (
	SHA1 Algorithm = iota
	SHA256
	SHA512
)

func (a Algorithm) new() func() hash.Hash {
	switch a {
	case SHA256:
		return sha256.New
	case SHA512:
		return sha512.New
	default:
		return sha1.New
	}
}

// Config carries the parameters of a TOTP secret. Defaults match the de-facto
// "authenticator app" standard: SHA-1, 6 digits, 30s period.
type Config struct {
	Secret    []byte // raw secret bytes (already base32-decoded)
	Digits    int    // typically 6 or 8
	Period    int    // typically 30 seconds
	Algorithm Algorithm
}

// Default applies the conventional fallbacks for zero-valued fields.
func (c Config) withDefaults() Config {
	if c.Digits == 0 {
		c.Digits = 6
	}
	if c.Period == 0 {
		c.Period = 30
	}
	return c
}

// HOTP computes the HMAC-based OTP for a given counter (RFC 4226). Exposed
// for completeness and testing; most callers want TOTP.
func HOTP(secret []byte, counter uint64, digits int, alg Algorithm) string {
	mac := hmac.New(alg.new(), secret)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 §5.3).
	offset := int(sum[len(sum)-1] & 0x0F)
	bin := (uint32(sum[offset]&0x7F) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// TOTP returns the code for the given moment in time.
func TOTP(c Config, at time.Time) string {
	c = c.withDefaults()
	counter := uint64(at.Unix() / int64(c.Period))
	return HOTP(c.Secret, counter, c.Digits, c.Algorithm)
}

// Now is a convenience shortcut.
func Now(c Config) string { return TOTP(c, time.Now()) }

// DecodeBase32Secret accepts the user-friendly base32 form ("JBSWY3DPEHPK3PXP",
// optionally with spaces and lowercase) and returns the raw bytes.
func DecodeBase32Secret(s string) ([]byte, error) {
	clean := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "-", ""))
	// Pad to multiple of 8 — most secrets are stored unpadded.
	if pad := len(clean) % 8; pad != 0 {
		clean += strings.Repeat("=", 8-pad)
	}
	return base32.StdEncoding.DecodeString(clean)
}
