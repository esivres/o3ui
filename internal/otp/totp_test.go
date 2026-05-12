package otp_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/otp"
)

// Test vectors from RFC 6238 Appendix B.
//
//	Secret SHA-1:   "12345678901234567890" (20 bytes ASCII)
//	Secret SHA-256: 32-byte ASCII
//	Secret SHA-512: 64-byte ASCII
//
// All produce 8-digit codes with period=30.
func TestTOTP_RFC6238Vectors(t *testing.T) {
	sha1Secret := []byte("12345678901234567890")
	sha256Secret := []byte("12345678901234567890123456789012")
	sha512Secret := []byte("1234567890123456789012345678901234567890123456789012345678901234")

	cases := []struct {
		name     string
		ts       int64
		alg      otp.Algorithm
		secret   []byte
		expected string
	}{
		{"sha1@59", 59, otp.SHA1, sha1Secret, "94287082"},
		{"sha256@59", 59, otp.SHA256, sha256Secret, "46119246"},
		{"sha512@59", 59, otp.SHA512, sha512Secret, "90693936"},
		{"sha1@1111111109", 1111111109, otp.SHA1, sha1Secret, "07081804"},
		{"sha1@1111111111", 1111111111, otp.SHA1, sha1Secret, "14050471"},
		{"sha1@1234567890", 1234567890, otp.SHA1, sha1Secret, "89005924"},
		{"sha1@2000000000", 2000000000, otp.SHA1, sha1Secret, "69279037"},
		{"sha1@20000000000", 20000000000, otp.SHA1, sha1Secret, "65353130"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := otp.TOTP(otp.Config{
				Secret:    tc.secret,
				Digits:    8,
				Period:    30,
				Algorithm: tc.alg,
			}, time.Unix(tc.ts, 0))
			require.Equal(t, tc.expected, code)
		})
	}
}

// HOTP test vectors from RFC 4226 Appendix D — counter 0..9, secret
// "12345678901234567890", 6 digits, SHA-1.
func TestHOTP_RFC4226Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	expected := []string{
		"755224", "287082", "359152", "969429", "338314",
		"254676", "287922", "162583", "399871", "520489",
	}
	for i, want := range expected {
		require.Equal(t, want, otp.HOTP(secret, uint64(i), 6, otp.SHA1),
			"counter=%d", i)
	}
}

func TestTOTP_DefaultsApplied(t *testing.T) {
	// Period and digits left at zero must default to 30s / 6 digits.
	code := otp.TOTP(otp.Config{Secret: []byte("12345678901234567890")}, time.Unix(59, 0))
	require.Len(t, code, 6, "default digits must be 6")
}

func TestDecodeBase32Secret(t *testing.T) {
	// "Hello!" base32 = JBSWY3DPEHPK3PXP — well-known test vector.
	cases := []string{
		"JBSWY3DPEHPK3PXP",
		"jbswy3dpehpk3pxp",    // case-insensitive
		"JBSW Y3DP EHPK 3PXP", // spaces
		"JBSW-Y3DP-EHPK-3PXP", // dashes
	}
	for _, in := range cases {
		out, err := otp.DecodeBase32Secret(in)
		require.NoError(t, err, "input %q", in)
		require.NotEmpty(t, out)
	}

	// Garbage rejected.
	_, err := otp.DecodeBase32Secret("not-base32-!!!")
	require.Error(t, err)
}
