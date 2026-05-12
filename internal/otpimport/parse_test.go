package otpimport_test

import (
	"encoding/base32"
	"encoding/base64"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/otp"
	"github.com/esivres/openvpn3ui/internal/otpimport"
)

func TestParseURI_OTPAuth_Standard(t *testing.T) {
	uri := "otpauth://totp/Example:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Example&algorithm=SHA1&digits=6&period=30"
	got, err := otpimport.ParseURI(uri)
	require.NoError(t, err)
	require.Len(t, got, 1)
	a := got[0]
	require.Equal(t, "Example", a.Issuer)
	require.Equal(t, "alice@example.com", a.Name)
	require.Equal(t, "JBSWY3DPEHPK3PXP", a.Secret)
	require.Equal(t, 6, a.Digits)
	require.Equal(t, 30, a.Period)
	require.Equal(t, otp.SHA1, a.Algorithm)
	require.False(t, a.IsHOTP)
}

func TestParseURI_OTPAuth_DefaultsAndAlgs(t *testing.T) {
	// No digits/period/algorithm → defaults. Algorithm SHA256 honoured.
	uri := "otpauth://totp/MyService:bob?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&algorithm=SHA256"
	got, err := otpimport.ParseURI(uri)
	require.NoError(t, err)
	a := got[0]
	require.Equal(t, "bob", a.Name)
	require.Equal(t, "MyService", a.Issuer)
	require.Equal(t, otp.SHA256, a.Algorithm)
	require.Equal(t, 6, a.Digits)
	require.Equal(t, 30, a.Period)
}

func TestParseURI_OTPAuth_HOTP(t *testing.T) {
	uri := "otpauth://hotp/Acme:carol?secret=JBSWY3DPEHPK3PXP&counter=10"
	got, err := otpimport.ParseURI(uri)
	require.NoError(t, err)
	require.True(t, got[0].IsHOTP)
}

func TestParseURI_OTPAuth_MissingSecretRejected(t *testing.T) {
	_, err := otpimport.ParseURI("otpauth://totp/foo?issuer=bar")
	require.Error(t, err)
}

func TestParseURI_UnknownScheme(t *testing.T) {
	_, err := otpimport.ParseURI("https://example.com/?secret=x")
	require.Error(t, err)
}

func TestAccount_Label(t *testing.T) {
	require.Equal(t, "Acme: alice", otpimport.Account{Issuer: "Acme", Name: "alice"}.Label())
	require.Equal(t, "alice", otpimport.Account{Name: "alice"}.Label())
	require.Equal(t, "Acme", otpimport.Account{Issuer: "Acme"}.Label())
}

// ----- migration URI tests ------------------------------------------------
//
// We synthesise a Google Authenticator-style payload by encoding the wire
// format ourselves. This keeps the test self-contained and exercises the
// exact bytes a real export would produce.

// pbBuilder is a tiny protobuf encoder (length-delimited + varint only).
type pbBuilder []byte

func (b *pbBuilder) varint(v uint64) {
	for v >= 0x80 {
		*b = append(*b, byte(v)|0x80)
		v >>= 7
	}
	*b = append(*b, byte(v))
}
func (b *pbBuilder) tag(field, wire int) { b.varint(uint64(field<<3 | wire)) }
func (b *pbBuilder) bytesField(field int, v []byte) {
	b.tag(field, 2)
	b.varint(uint64(len(v)))
	*b = append(*b, v...)
}
func (b *pbBuilder) stringField(field int, v string) { b.bytesField(field, []byte(v)) }
func (b *pbBuilder) varintField(field int, v uint64) {
	b.tag(field, 0)
	b.varint(v)
}

func encodeMigrationURI(t *testing.T, accounts []otpimport.Account) string {
	t.Helper()
	var top pbBuilder
	for _, a := range accounts {
		var inner pbBuilder
		raw, err := base32.StdEncoding.DecodeString(a.Secret + padForBase32(a.Secret))
		require.NoError(t, err)
		inner.bytesField(1, raw)
		inner.stringField(2, a.Name)
		inner.stringField(3, a.Issuer)
		switch a.Algorithm {
		case otp.SHA256:
			inner.varintField(4, 2)
		case otp.SHA512:
			inner.varintField(4, 3)
		default:
			inner.varintField(4, 1)
		}
		if a.Digits == 8 {
			inner.varintField(5, 2)
		} else {
			inner.varintField(5, 1)
		}
		if a.IsHOTP {
			inner.varintField(6, 1)
		} else {
			inner.varintField(6, 2)
		}
		top.bytesField(1, []byte(inner))
	}
	top.varintField(2, 1) // version
	encoded := base64.StdEncoding.EncodeToString([]byte(top))
	// QueryEscape so `+` and `/` survive a round-trip through net/url —
	// matches what a correctly-formed Google Authenticator export does.
	return "otpauth-migration://offline?data=" + url.QueryEscape(encoded)
}

func padForBase32(s string) string {
	if pad := len(s) % 8; pad != 0 {
		out := ""
		for i := 0; i < 8-pad; i++ {
			out += "="
		}
		return out
	}
	return ""
}

func TestParseURI_Migration_SingleAccount(t *testing.T) {
	uri := encodeMigrationURI(t, []otpimport.Account{
		{Issuer: "Acme", Name: "alice", Secret: "JBSWY3DPEHPK3PXP", Digits: 6, Algorithm: otp.SHA1},
	})

	got, err := otpimport.ParseURI(uri)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Acme", got[0].Issuer)
	require.Equal(t, "alice", got[0].Name)
	require.Equal(t, "JBSWY3DPEHPK3PXP", got[0].Secret)
	require.Equal(t, 6, got[0].Digits)
	require.Equal(t, otp.SHA1, got[0].Algorithm)
}

func TestParseURI_Migration_MultipleAccountsAndAlgs(t *testing.T) {
	in := []otpimport.Account{
		{Issuer: "A", Name: "a", Secret: "JBSWY3DPEHPK3PXP", Algorithm: otp.SHA1, Digits: 6},
		{Issuer: "B", Name: "b", Secret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", Algorithm: otp.SHA256, Digits: 8},
		{Issuer: "C", Name: "c", Secret: "JBSWY3DPEHPK3PXP", Algorithm: otp.SHA512, IsHOTP: true},
	}
	uri := encodeMigrationURI(t, in)

	got, err := otpimport.ParseURI(uri)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "B", got[1].Issuer)
	require.Equal(t, otp.SHA256, got[1].Algorithm)
	require.Equal(t, 8, got[1].Digits)
	require.True(t, got[2].IsHOTP)
	require.Equal(t, otp.SHA512, got[2].Algorithm)
}

func TestParseURI_Migration_BadBase64(t *testing.T) {
	_, err := otpimport.ParseURI("otpauth-migration://offline?data=!!!notbase64!!!")
	require.Error(t, err)
}

func TestParseURI_Migration_MissingData(t *testing.T) {
	_, err := otpimport.ParseURI("otpauth-migration://offline")
	require.Error(t, err)
}

// Sanity: a parsed migration secret must regenerate the same TOTP code as
// the equivalent otpauth:// URI. End-to-end check that base32 round-trips
// through bytes and back.
func TestMigration_RoundTripsToSameTOTP(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	migURI := encodeMigrationURI(t, []otpimport.Account{
		{Name: "alice", Secret: secret, Digits: 6, Algorithm: otp.SHA1},
	})
	authURI := "otpauth://totp/alice?secret=" + secret

	mig, err := otpimport.ParseURI(migURI)
	require.NoError(t, err)
	auth, err := otpimport.ParseURI(authURI)
	require.NoError(t, err)
	require.Equal(t, mig[0].Secret, auth[0].Secret)
}
