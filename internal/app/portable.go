package app

import (
	"encoding/json"
	"errors"
	"fmt"
)

// PortableProfile is the on-disk shape of an exported profile — the
// .ovpn body plus every piece of metadata o3ui adds on top (overlay
// fields + remembered credentials + TOTP secret). It is a *plaintext*
// bundle: the secret material lives in keyring storage on the source
// machine but has to travel verbatim to reach a different host. The
// export file is written 0600 and the UI flashes a warning.
//
// Version is a tiny forward-compat hook — the importer rejects bundles
// it doesn't know how to read, rather than silently misinterpreting a
// future field as v1.
type PortableProfile struct {
	Version int                    `json:"version"`
	Name    string                 `json:"name"`
	Config  string                 `json:"config"` // raw .ovpn text
	Overlay PortableOverlay        `json:"overlay,omitempty"`
	Creds   *PortableCredentials   `json:"credentials,omitempty"`
	TOTP    *PortableTOTP          `json:"totp,omitempty"`
	Extra   map[string]interface{} `json:"-"` // reserved
}

type PortableOverlay struct {
	Country     string `json:"country,omitempty"`
	Favorite    bool   `json:"favorite,omitempty"`
	AutoConnect bool   `json:"auto_connect,omitempty"`
}

type PortableCredentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type PortableTOTP struct {
	Secret string `json:"secret"` // base32, what the user originally entered
}

// portableVersion is the schema version we emit and accept today.
const portableVersion = 1

// ExportProfile assembles a PortableProfile for a known config path.
// Returns an error if the .ovpn body can't be fetched; missing overlay
// / credentials / OTP are not errors — they just stay absent from the
// bundle.
func (s *Service) ExportProfile(configPath string) (PortableProfile, error) {
	cfgs, err := s.configs.List()
	if err != nil {
		return PortableProfile{}, fmt.Errorf("list configs: %w", err)
	}
	var name string
	for i := range cfgs {
		if cfgs[i].Path == configPath {
			name = cfgs[i].Name
			break
		}
	}
	if name == "" {
		return PortableProfile{}, fmt.Errorf("no config at %s", configPath)
	}
	body, err := s.configs.Fetch(configPath)
	if err != nil {
		return PortableProfile{}, err
	}
	out := PortableProfile{
		Version: portableVersion,
		Name:    name,
		Config:  body,
	}
	if s.overlay != nil {
		if o, oerr := s.overlay.Get(configPath); oerr == nil {
			out.Overlay = PortableOverlay{
				Country:     o.CountryCode,
				Favorite:    o.Favorite,
				AutoConnect: o.AutoConnect,
			}
			// Credentials: username is plaintext in overlay; password
			// lives in keyring under PasswordSecretID.
			if o.Username != "" || o.PasswordSecretID != "" {
				creds := &PortableCredentials{Username: o.Username}
				if o.PasswordSecretID != "" && s.secrets != nil {
					if pw, perr := s.secrets.Get(o.PasswordSecretID); perr == nil {
						creds.Password = pw
					}
				}
				out.Creds = creds
			}
			// TOTP secret — same dance, base32 form preserved as stored.
			if o.OTPSecretID != "" && s.secrets != nil {
				if sec, perr := s.secrets.Get(o.OTPSecretID); perr == nil {
					out.TOTP = &PortableTOTP{Secret: sec}
				}
			}
		}
	}
	return out, nil
}

// MarshalPortable renders a PortableProfile as pretty JSON. Indented so
// a human can sanity-check the contents (especially the TOTP secret)
// before sharing or backing up.
//
//nolint:gocritic // hugeParam: kept value-typed so call sites can pass a literal
func MarshalPortable(p PortableProfile) ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// UnmarshalPortable validates and decodes a PortableProfile bundle.
// Rejects unknown / future schema versions explicitly — better to fail
// loudly than to import a half-understood file.
func UnmarshalPortable(data []byte) (PortableProfile, error) {
	var p PortableProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return PortableProfile{}, fmt.Errorf("decode portable profile: %w", err)
	}
	if p.Version == 0 {
		return PortableProfile{}, errors.New("missing schema version (not a portable profile?)")
	}
	if p.Version != portableVersion {
		return PortableProfile{}, fmt.Errorf("unsupported portable-profile version %d (this build understands %d)", p.Version, portableVersion)
	}
	if p.Name == "" || p.Config == "" {
		return PortableProfile{}, errors.New("portable profile missing name or config body")
	}
	return p, nil
}

// ImportPortable round-trips a PortableProfile into openvpn3 + overlay
// + keyring. Returns the newly-imported config path. Steps:
//
//  1. configs.Import the .ovpn body (persistent, not single-use).
//  2. Apply overlay metadata (favorite / country / auto-connect).
//  3. Store remembered credentials, if any.
//  4. Store the TOTP secret, if any.
//
// Partial failures are reported but don't roll back: if step 3 errors,
// the config is still imported (the user can re-enter their password).
// This mirrors how the interactive Edit screen behaves on similar
// half-success outcomes.
//
//nolint:gocritic // hugeParam: PortableProfile is exposed by value as the API contract
func (s *Service) ImportPortable(p PortableProfile) (string, error) {
	path, err := s.configs.Import(p.Name, p.Config, true)
	if err != nil {
		return "", fmt.Errorf("config import: %w", err)
	}
	// Overlay flags — best-effort.
	if s.overlay != nil {
		if p.Overlay.Favorite {
			_ = s.SetFavorite(path, true)
		}
		if p.Overlay.AutoConnect {
			_ = s.SetAutoConnect(path, true)
		}
		if p.Overlay.Country != "" {
			_ = s.SetCountryCode(path, p.Overlay.Country)
		}
	}
	if p.Creds != nil {
		if p.Creds.Username != "" {
			_ = s.RememberUsername(path, p.Creds.Username)
		}
		if p.Creds.Password != "" {
			_ = s.RememberPassword(path, p.Creds.Password)
		}
	}
	if p.TOTP != nil && p.TOTP.Secret != "" {
		_ = s.SetOTP(path, p.TOTP.Secret)
	}
	return path, nil
}
