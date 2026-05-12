package tui

import (
	"context"
	"errors"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
)

// promptRequest is the cross-goroutine bridge between the connect goroutine
// (running Service.Connect → Auth.Provide) and the bubbletea event loop.
// Root catches the request, switches to the auth modal, and writes the
// reply back through Reply when the user submits or cancels.
type promptRequest struct {
	ConfigPath string
	Prompt     ovpn.InputPrompt
	Reply      chan promptReply
}

type promptReply struct {
	Value string
	Err   error
}

// Prompter implements app.Prompter on top of a bubbletea Program. The
// Program reference is bound after construction (chicken-and-egg: Service
// needs the Prompter to assemble its Auth chain; the Program needs Root,
// which needs the Service). Until BindSend is called, Ask returns an
// error and Connect attempts will fail cleanly.
type Prompter struct {
	mu   sync.Mutex
	send func(tea.Msg)
}

// NewPrompter creates an unbound Prompter. Pair with BindSend after the
// tea.Program is constructed.
func NewPrompter() *Prompter { return &Prompter{} }

// BindSend wires the function used to inject messages into the bubbletea
// loop — typically (*tea.Program).Send. Safe to call once at startup.
func (p *Prompter) BindSend(send func(tea.Msg)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.send = send
}

// Ask satisfies app.Prompter. Blocks until the user submits, cancels,
// or ctx fires — the third option is what makes Esc-from-connecting-
// screen actually free the goroutine. Without context the prompt
// would otherwise wait on a reply channel that nobody reads (root
// already routed past us when the screen was dismissed).
func (p *Prompter) Ask(ctx context.Context, configPath string, prompt ovpn.InputPrompt) (string, error) {
	p.mu.Lock()
	send := p.send
	p.mu.Unlock()
	if send == nil {
		return "", errors.New("UI not ready to handle prompts")
	}
	reply := make(chan promptReply, 1)
	send(promptRequest{ConfigPath: configPath, Prompt: prompt, Reply: reply})
	select {
	case rep := <-reply:
		return rep.Value, rep.Err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Compile-time guard: *Prompter implements app.Prompter.
var _ app.Prompter = (*Prompter)(nil)
