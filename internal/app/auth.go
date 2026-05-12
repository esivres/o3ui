package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/esivres/openvpn3ui/internal/otp"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// authNotApplicable is returned by an Auth layer that cannot answer a given
// prompt. ChainAuth uses it to fall through to the next layer.
var errAuthNotApplicable = errors.New("auth: not applicable")

// IsNotApplicable reports whether an error came from an Auth layer that
// declined to answer. Useful when writing custom chains.
func IsNotApplicable(err error) bool { return errors.Is(err, errAuthNotApplicable) }

// AuthFunc is a convenience adapter so a closure can satisfy Auth.
type AuthFunc func(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error)

func (f AuthFunc) Provide(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error) {
	return f(ctx, configPath, p)
}

// ChainAuth tries each layer in order. A layer signals "skip me" by
// returning an error wrapping errAuthNotApplicable.
type ChainAuth struct {
	Layers []Auth
}

func (c ChainAuth) Provide(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error) {
	var lastErr error
	for _, l := range c.Layers {
		v, err := l.Provide(ctx, configPath, p)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, errAuthNotApplicable) {
			return "", err // hard refusal, stop
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errAuthNotApplicable
	}
	return "", lastErr
}

// secretLookup is the slice of secrets.Store this layer needs.
type secretLookup interface {
	Get(id string) (string, error)
}

// overlayLookup is the slice of overlay.Store this layer needs.
type overlayLookup interface {
	Get(configPath string) (overlay.Overlay, error)
}

// AutoTOTPAuth answers OTP/static-challenge prompts by generating a TOTP
// from a secret stored in the OS keyring, keyed via the per-config overlay.
// For any other prompt it returns errAuthNotApplicable so the next layer
// (typically a UI dialog) can answer.
type AutoTOTPAuth struct {
	Overlay overlayLookup
	Secrets secretLookup
	Now     func() time.Time // injectable for tests
}

func NewAutoTOTPAuth(o overlayLookup, s secretLookup) *AutoTOTPAuth {
	return &AutoTOTPAuth{Overlay: o, Secrets: s, Now: time.Now}
}

// looksLikeOTP guesses whether a prompt is asking for a one-time code.
// openvpn3 surfaces challenge prompts with names that vary by server, so
// we go by substring match against well-known keywords.
func looksLikeOTP(p ovpn.InputPrompt) bool {
	hay := strings.ToLower(p.Name + " " + p.Description)
	keywords := []string{"otp", "static_challenge", "static challenge", "auth_pin", "auth pin", "totp", "token", "two-factor", "2fa"}
	for _, k := range keywords {
		if strings.Contains(hay, k) {
			return true
		}
	}
	return false
}

// StoredCredentialsAuth answers username/password prompts from the values
// the user previously chose to remember. Falls through (errAuthNotApplicable)
// for OTP prompts and for any prompt whose stored value is missing — the
// next layer (typically PromptAuth) gets a chance to handle them.
type StoredCredentialsAuth struct {
	Overlay overlayLookup
	Secrets secretLookup
}

func NewStoredCredentialsAuth(o overlayLookup, s secretLookup) *StoredCredentialsAuth {
	return &StoredCredentialsAuth{Overlay: o, Secrets: s}
}

func (a *StoredCredentialsAuth) Provide(_ context.Context, configPath string, p ovpn.InputPrompt) (string, error) {
	name := strings.ToLower(p.Name)
	o, err := a.Overlay.Get(configPath)
	if err != nil {
		return "", errAuthNotApplicable
	}
	switch {
	case strings.Contains(name, "user") && o.Username != "":
		return o.Username, nil
	case strings.Contains(name, "pass") && o.PasswordSecretID != "":
		v, err := a.Secrets.Get(o.PasswordSecretID)
		if err == nil && v != "" {
			return v, nil
		}
	}
	return "", errAuthNotApplicable
}

func (a *AutoTOTPAuth) Provide(_ context.Context, configPath string, p ovpn.InputPrompt) (string, error) {
	if !looksLikeOTP(p) {
		return "", errAuthNotApplicable
	}
	o, err := a.Overlay.Get(configPath)
	if err != nil || o.OTPSecretID == "" {
		return "", errAuthNotApplicable
	}
	raw, err := a.Secrets.Get(o.OTPSecretID)
	if err != nil {
		return "", errAuthNotApplicable
	}
	secret, err := otp.DecodeBase32Secret(raw)
	if err != nil {
		return "", errAuthNotApplicable
	}
	now := a.Now
	if now == nil {
		now = time.Now
	}
	return otp.TOTP(otp.Config{Secret: secret}, now()), nil
}
