package app

import (
	"context"
	"errors"

	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// Prompter is the contract the UI implements to answer a single openvpn3
// UserInput prompt — typically by showing a modal with one input field.
// Returning ErrPromptCancelled aborts the connect; any other error is
// surfaced as-is.
type Prompter interface {
	// Ask blocks until the user submits a value, cancels, or ctx fires.
	// configPath is passed so the UI can show "for Frankfurt — Work"
	// in the dialog. ctx cancellation lets the caller (Service.Connect
	// driven by a TUI Esc or CLI timeout) tear down a stuck prompt
	// without leaking the goroutine waiting on a reply channel.
	Ask(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error)
}

// PrompterFunc lets a closure satisfy Prompter.
type PrompterFunc func(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error)

func (f PrompterFunc) Ask(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error) {
	return f(ctx, configPath, p)
}

// ErrPromptCancelled is what a Prompter returns when the user dismisses
// the dialog without entering a value. ChainAuth treats it as a hard
// refusal — we don't fall through to the next layer.
var ErrPromptCancelled = errors.New("prompt cancelled")

// PromptAuth is the catch-all Auth layer: it forwards every prompt to a
// Prompter. Place it last in a ChainAuth so prompts that earlier layers
// (e.g. AutoTOTPAuth) can answer don't bother the user.
type PromptAuth struct {
	Prompter Prompter
}

func NewPromptAuth(p Prompter) *PromptAuth { return &PromptAuth{Prompter: p} }

func (a *PromptAuth) Provide(ctx context.Context, configPath string, p ovpn.InputPrompt) (string, error) {
	if a.Prompter == nil {
		// Nothing can answer — same outcome as no Auth at all, but with a
		// nicer error message for the UI.
		return "", ErrPromptCancelled
	}
	return a.Prompter.Ask(ctx, configPath, p)
}
