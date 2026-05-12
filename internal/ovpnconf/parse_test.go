package ovpnconf_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/ovpnconf"
)

func TestParse_SimpleClient(t *testing.T) {
	const cfg = `client
dev tun
proto udp
remote vpn.example.com 1194
remote backup.example.com
cipher AES-256-CBC
auth SHA256
auth-user-pass
remote-cert-tls server
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.Len(t, p.Remotes, 2)

	r0 := p.Remotes[0]
	require.Equal(t, "vpn.example.com", r0.Host)
	require.Equal(t, 1194, r0.Port)
	require.Equal(t, "udp", r0.Proto)

	// Second remote inherits port + proto from file-level defaults.
	r1 := p.Remotes[1]
	require.Equal(t, "backup.example.com", r1.Host)
	require.Equal(t, 1194, r1.Port)
	require.Equal(t, "udp", r1.Proto)

	require.Equal(t, "AES-256-CBC", p.Cipher)
	require.Equal(t, "SHA256", p.AuthDigest)
	require.True(t, p.NeedUserPass)
	require.Equal(t, "server", p.RemoteCertTLS)
	require.Equal(t, "user/pass", p.AuthMethod())
}

func TestParse_PerRemoteOverrides(t *testing.T) {
	const cfg = `proto udp
port 1194
remote vpn.example.com 443 tcp
remote alt.example.com 1194 udp
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.Equal(t, "tcp", p.Remotes[0].Proto)
	require.Equal(t, 443, p.Remotes[0].Port)
	require.Equal(t, "udp", p.Remotes[1].Proto)
	require.Equal(t, 1194, p.Remotes[1].Port)
}

func TestParse_DataCiphersWins(t *testing.T) {
	// data-ciphers (modern) should win over the legacy cipher directive
	// regardless of order, because it represents the negotiated list.
	const cfg = `cipher AES-256-CBC
data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.Equal(t, "AES-256-GCM", p.Cipher)
}

func TestParse_StaticChallenge(t *testing.T) {
	const cfg = `auth-user-pass
static-challenge "Enter Authenticator Code" 1
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.Equal(t, "Enter Authenticator Code", p.StaticChallenge)
	require.True(t, p.StaticChallengeEcho)
	require.Equal(t, "user/pass+TOTP", p.AuthMethod())
}

func TestParse_InlineBlocks(t *testing.T) {
	const cfg = `client
remote vpn.example.com 1194
<ca>
-----BEGIN CERTIFICATE-----
fake-ca-bytes
-----END CERTIFICATE-----
</ca>
<key>
-----BEGIN PRIVATE KEY-----
fake-key
-----END PRIVATE KEY-----
</key>
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.True(t, p.HasInlineCA)
	require.False(t, p.HasInlineCert)
	require.True(t, p.HasInlineKey)
}

func TestParse_EmbeddedCertParsed(t *testing.T) {
	pemBytes, expectedCN, expectedNotAfter := makeTestCertPEM(t)

	cfg := "client\nremote vpn.example.com 1194\n<cert>\n" + string(pemBytes) + "\n</cert>\n"
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.True(t, p.HasInlineCert)
	require.NotNil(t, p.EmbeddedCert)
	require.Equal(t, expectedCN, p.EmbeddedCert.CommonName)
	require.WithinDuration(t, expectedNotAfter, p.EmbeddedCert.NotAfter, time.Second)
	require.Contains(t, p.AuthMethod(), "cert")
}

func TestParse_CommentsAndBlanksIgnored(t *testing.T) {
	const cfg = `# comment
; semicolon comment
client

remote vpn.example.com 1194 # trailing not stripped after quotes-or-no
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.Equal(t, "vpn.example.com", p.Remotes[0].Host)
	require.Equal(t, 1194, p.Remotes[0].Port)
}

func TestParse_UnterminatedInlineFails(t *testing.T) {
	const cfg = `<ca>
-----BEGIN CERTIFICATE-----
truncated...
`
	_, err := ovpnconf.ParseString(cfg)
	require.Error(t, err)
}

func TestParse_NoRemotes(t *testing.T) {
	p, err := ovpnconf.ParseString("client\nproto tcp\n")
	require.NoError(t, err)
	require.Empty(t, p.Remotes)
	require.Equal(t, ovpnconf.Remote{}, p.PrimaryRemote())
}

func TestParse_ProtoNormalisation(t *testing.T) {
	const cfg = `proto tcp4-client
remote a 443
`
	p, err := ovpnconf.ParseString(cfg)
	require.NoError(t, err)
	require.Equal(t, "tcp", p.Remotes[0].Proto)
}

func TestAuthMethod_Defaults(t *testing.T) {
	require.Equal(t, "anonymous", (&ovpnconf.Profile{}).AuthMethod())
}

// makeTestCertPEM generates a self-signed certificate so the cert-parsing
// path is exercised end-to-end without checking in any real material.
func makeTestCertPEM(t *testing.T) ([]byte, string, time.Time) {
	t.Helper()

	const cn = "openvpn3ui-test-user"
	notAfter := time.Now().Add(365 * 24 * time.Hour).UTC().Truncate(time.Second)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, pem.Encode(stringWriter{&buf}, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return []byte(buf.String()), cn, notAfter
}

// stringWriter adapts strings.Builder to io.Writer.
type stringWriter struct{ b *strings.Builder }

func (w stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
