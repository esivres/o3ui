// Package components — diagnose turns the (often cryptic) error string
// openvpn3 hands back on a failed Connect into a category + a concrete
// next step. Centralising the pattern matching here keeps the rules
// inspectable in one place; UI surfaces (Connecting screen, future
// notifications) all call the same function and render the same advice.
package components

import "strings"

// Diagnosis is what the failure screen shows: a short category label,
// a one-line plain-text description, and the suggested next step. Hint
// is empty when we don't recognise the failure — better to show
// nothing than to invent advice that doesn't apply.
type Diagnosis struct {
	Category string // e.g. "auth", "tls", "network", "config"
	Title    string // human-readable cause
	Hint     string // suggested action; empty if we don't have one
}

// Diagnose runs the error message through known openvpn3 failure
// signatures. The matchers are deliberately permissive — openvpn3
// wraps the underlying message in its own prefixes (e.g. "Connect: ...",
// "Ready=... ; queue=..."), so we substring-match rather than
// pattern-anchor. Tested cases come straight from the openvpn3 v27
// log output we've seen in the field.
func Diagnose(err error) Diagnosis {
	if err == nil {
		return Diagnosis{}
	}
	msg := err.Error()
	low := strings.ToLower(msg)

	switch {
	case containsAny(low, "auth_failed", "auth-failed", "authentication failed",
		"server rejected", "user/password"):
		return Diagnosis{
			Category: "auth",
			Title:    "Credentials rejected by the server.",
			Hint:     "Open this profile (e), press u/p to re-enter username / password, then try again. If TOTP is in use, check that the secret hasn't rotated.",
		}
	case containsAny(low, "tls handshake", "tls error", "tls failed",
		"certificate has expired", "certificate verify failed",
		"unable to get local issuer", "ssl_error"):
		return Diagnosis{
			Category: "tls",
			Title:    "TLS handshake failed.",
			Hint:     "Most often a clock skew on this host or an expired certificate on the server. Run `timedatectl status` to verify time sync; if the cert is the issue, the server admin needs to roll it.",
		}
	case containsAny(low, "network is unreachable", "no route to host",
		"could not resolve", "name or service not known",
		"temporary failure in name resolution", "dns"):
		return Diagnosis{
			Category: "network",
			Title:    "Couldn't reach the VPN endpoint.",
			Hint:     "Confirm the underlying internet works (curl https://1.1.1.1) and DNS resolves the VPN host. If you're on a captive portal, sign in first.",
		}
	case containsAny(low, "connection refused", "connection reset",
		"connection timed out", "no connection could be made"):
		return Diagnosis{
			Category: "network",
			Title:    "VPN port unreachable.",
			Hint:     "The server is up but the port is blocked or filtered. Try a different transport (UDP↔TCP, alternate port) if your profile supports it, or check upstream firewall rules.",
		}
	case containsAny(low, "user cancelled", "context canceled", "context cancelled"):
		return Diagnosis{
			Category: "cancelled",
			Title:    "Connect was cancelled.",
			Hint:     "No action needed — press Esc to return.",
		}
	case containsAny(low, "object does not exist", "servicestart", "starts ervicebyname"):
		return Diagnosis{
			Category: "service",
			Title:    "openvpn3 backend not available.",
			Hint:     "`systemctl status openvpn3-session.service` to see why D-Bus activation failed. Reinstall openvpn3 if the unit is missing.",
		}
	}

	return Diagnosis{
		Category: "unknown",
		Title:    "Connect failed.",
		Hint:     "", // no canned advice — let the user read the raw error and decide
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
