// Package ovpnconf parses the subset of an .ovpn / OpenVPN client
// configuration file we need to drive the UI: remote endpoints, cipher,
// auth method, embedded certificate metadata. We do not validate the
// config or attempt to round-trip it — openvpn3 itself is the source of
// truth at connect time.
package ovpnconf

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Remote is a single `remote` directive in an .ovpn file.
type Remote struct {
	Host  string
	Port  int    // resolved (line-level → file-level → 1194)
	Proto string // "udp" or "tcp" (line-level → file-level → "udp")
}

// CertInfo summarises the first inline <cert>...</cert> block.
type CertInfo struct {
	CommonName string
	NotAfter   time.Time
}

// Profile is the structured view of the parsed file.
type Profile struct {
	Remotes []Remote

	// Cipher is the primary data-channel cipher. Preference order:
	// first entry of `data-ciphers` (modern), then `cipher` (legacy).
	Cipher string

	// AuthDigest is the value of the `auth` directive (HMAC algorithm
	// for the data channel). Empty if not specified.
	AuthDigest string

	// NeedUserPass mirrors the presence of `auth-user-pass`.
	NeedUserPass bool

	// StaticChallenge is the prompt text for `static-challenge "<text>" <echo>`.
	// Empty when absent. Echo is the echo flag (1 = visible input).
	StaticChallenge     string
	StaticChallengeEcho bool

	// RemoteCertTLS is the value of `remote-cert-tls` ("server" / "client").
	RemoteCertTLS string

	// HasInlineCA / HasInlineCert / HasInlineKey reflect the presence of
	// the corresponding <...>...</...> PEM blocks.
	HasInlineCA   bool
	HasInlineCert bool
	HasInlineKey  bool

	// EmbeddedCert is parsed from the first <cert> block, if present.
	EmbeddedCert *CertInfo
}

// AuthMethod returns a short human-readable summary of how the user
// authenticates with this profile. Drives the "auth" badge in the UI.
func (p *Profile) AuthMethod() string {
	parts := []string{}
	if p.HasInlineCert || p.HasInlineKey {
		parts = append(parts, "cert")
	}
	if p.NeedUserPass {
		parts = append(parts, "user/pass")
	}
	if p.StaticChallenge != "" {
		parts = append(parts, "TOTP")
	}
	if len(parts) == 0 {
		return "anonymous"
	}
	return strings.Join(parts, "+")
}

// PrimaryRemote returns the first Remote — typically what the UI shows in
// a one-line summary. Returns the zero value when no remotes are declared.
func (p *Profile) PrimaryRemote() Remote {
	if len(p.Remotes) == 0 {
		return Remote{}
	}
	return p.Remotes[0]
}

// Parse reads a .ovpn config and returns the structured Profile.
func Parse(r io.Reader) (*Profile, error) {
	p := &Profile{}

	// File-level fallbacks (overridden by per-`remote` line values).
	defaultPort := 1194
	defaultProto := "udp"

	type tagBuf struct {
		name string
		body strings.Builder
	}
	var inline *tagBuf

	sc := bufio.NewScanner(r)
	// .ovpn cert blocks can be a few KB; raise the line buffer ceiling.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()

		// Inside an inline block, copy lines verbatim until we see </tag>.
		if inline != nil {
			trimmed := strings.TrimSpace(line)
			if trimmed == "</"+inline.name+">" {
				p.consumeInline(inline.name, inline.body.String())
				inline = nil
				continue
			}
			inline.body.WriteString(line)
			inline.body.WriteByte('\n')
			continue
		}

		raw := strings.TrimSpace(line)
		if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, ";") {
			continue
		}

		// Inline block opener: <tag>
		if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") && !strings.HasPrefix(raw, "</") {
			name := strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">")
			inline = &tagBuf{name: name}
			continue
		}

		fields := splitConfigLine(raw)
		if len(fields) == 0 {
			continue
		}

		switch fields[0] {
		case "remote":
			rem, ok := parseRemote(fields[1:])
			if ok {
				p.Remotes = append(p.Remotes, rem)
			}
		case "port":
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					defaultPort = n
				}
			}
		case "proto":
			if len(fields) >= 2 {
				defaultProto = normaliseProto(fields[1])
			}
		case "cipher":
			if len(fields) >= 2 && p.Cipher == "" {
				p.Cipher = fields[1]
			}
		case "data-ciphers":
			if len(fields) >= 2 {
				// First entry wins; the rest is a fallback list.
				if first := strings.SplitN(fields[1], ":", 2)[0]; first != "" {
					p.Cipher = first
				}
			}
		case "auth":
			if len(fields) >= 2 {
				p.AuthDigest = fields[1]
			}
		case "auth-user-pass":
			p.NeedUserPass = true
		case "static-challenge":
			// Format: static-challenge "Prompt text" {0|1}
			if len(fields) >= 3 {
				p.StaticChallenge = strings.Trim(fields[1], `"`)
				p.StaticChallengeEcho = fields[2] == "1"
			} else if len(fields) == 2 {
				p.StaticChallenge = strings.Trim(fields[1], `"`)
			}
		case "remote-cert-tls":
			if len(fields) >= 2 {
				p.RemoteCertTLS = fields[1]
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if inline != nil {
		return nil, fmt.Errorf("unterminated inline block <%s>", inline.name)
	}

	// Apply file-level proto/port to remotes that didn't carry their own.
	for i := range p.Remotes {
		if p.Remotes[i].Port == 0 {
			p.Remotes[i].Port = defaultPort
		}
		if p.Remotes[i].Proto == "" {
			p.Remotes[i].Proto = defaultProto
		}
	}
	return p, nil
}

// ParseString is a string-input convenience wrapper.
func ParseString(s string) (*Profile, error) { return Parse(strings.NewReader(s)) }

func (p *Profile) consumeInline(tag, body string) {
	switch tag {
	case "ca":
		p.HasInlineCA = true
	case "cert":
		p.HasInlineCert = true
		if info := firstCertFromPEM([]byte(body)); info != nil {
			p.EmbeddedCert = info
		}
	case "key":
		p.HasInlineKey = true
	}
	// Other tags (tls-auth, tls-crypt, ...) are noted by absence of a
	// case branch; we don't need them yet but the parser still consumes
	// them gracefully.
}

func parseRemote(fields []string) (Remote, bool) {
	if len(fields) == 0 {
		return Remote{}, false
	}
	r := Remote{Host: fields[0]}
	if len(fields) >= 2 {
		if n, err := strconv.Atoi(fields[1]); err == nil {
			r.Port = n
		}
	}
	if len(fields) >= 3 {
		r.Proto = normaliseProto(fields[2])
	}
	return r, true
}

func normaliseProto(s string) string {
	s = strings.ToLower(s)
	switch s {
	case "udp", "udp4", "udp6":
		return "udp"
	case "tcp", "tcp-client", "tcp4", "tcp6", "tcp4-client", "tcp6-client":
		return "tcp"
	default:
		return s
	}
}

// splitConfigLine handles the basic OpenVPN tokenisation: whitespace
// separated, with double-quoted strings preserved as a single token. We do
// not aim for full shell compatibility — the directives we care about
// are straightforward.
func splitConfigLine(line string) []string {
	// Drop trailing inline comment.
	if i := strings.IndexAny(line, "#;"); i >= 0 {
		// But not when inside quotes — cheap check.
		if !strings.Contains(line[:i], `"`) {
			line = line[:i]
		}
	}

	var (
		out   []string
		buf   strings.Builder
		inStr bool
	)
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	for _, r := range line {
		switch {
		case r == '"':
			inStr = !inStr
			buf.WriteRune(r)
		case (r == ' ' || r == '\t') && !inStr:
			flush()
		default:
			buf.WriteRune(r)
		}
	}
	flush()
	return out
}

// firstCertFromPEM scans PEM blocks and returns metadata for the first
// CERTIFICATE block. Other block types (PRIVATE KEY, etc.) are skipped.
// Returns nil when no parseable cert is found — callers should treat that
// as "no cert info available", not an error.
func firstCertFromPEM(data []byte) *CertInfo {
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			return nil
		}
		data = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		return &CertInfo{
			CommonName: c.Subject.CommonName,
			NotAfter:   c.NotAfter,
		}
	}
}

// ErrEmpty is returned by helpers when the profile body has no usable content.
var ErrEmpty = errors.New("empty profile")
