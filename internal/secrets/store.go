// Package secrets is the keyring abstraction. Domain code never imports
// go-keyring directly so tests can pin behaviour with an in-memory Store.
package secrets

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// service is the keyring "service" namespace under which all openvpn3ui
// items live. Keep it stable — changing it orphans existing entries.
const service = "openvpn3ui"

// ErrNotFound is returned when no secret exists for the given id.
var ErrNotFound = errors.New("secret not found")

// Store is the abstraction over a credential storage backend. On Linux this
// is freedesktop Secret Service (GNOME Keyring / KWallet). It is keyed by an
// application-defined id — typically an openvpn3 config path plus a kind
// suffix such as ":totp".
type Store interface {
	Get(id string) (string, error)
	Set(id, value string) error
	Delete(id string) error
}

// SystemStore is the production implementation backed by zalando/go-keyring.
// On Linux, that talks to org.freedesktop.secrets over D-Bus.
type SystemStore struct{}

func New() *SystemStore { return &SystemStore{} }

func (SystemStore) Get(id string) (string, error) {
	v, err := keyring.Get(service, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keyring get %q: %w", id, err)
	}
	return v, nil
}

func (SystemStore) Set(id, value string) error {
	if err := keyring.Set(service, id, value); err != nil {
		return fmt.Errorf("keyring set %q: %w", id, err)
	}
	return nil
}

func (SystemStore) Delete(id string) error {
	err := keyring.Delete(service, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("keyring delete %q: %w", id, err)
	}
	return nil
}
