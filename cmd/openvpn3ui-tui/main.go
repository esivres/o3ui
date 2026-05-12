// openvpn3ui-tui is the entry point binary for the o3ui project. With
// arguments it acts as a CLI (status/list/connect/disconnect/desklet);
// without arguments it launches the Bubble Tea TUI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	openapp "github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/cli"
	"github.com/esivres/openvpn3ui/internal/overlay"
	"github.com/esivres/openvpn3ui/internal/ovpn"
	"github.com/esivres/openvpn3ui/internal/secrets"
	"github.com/esivres/openvpn3ui/internal/tui"
)

type sessionAdapter struct{ *ovpn.SessionManager }

func (a sessionAdapter) Control(path string) openapp.SessionControl {
	return a.SessionManager.Control(path)
}

// Get exposes ovpn.SessionManager.Get on the openapp.SessionBackend
// interface. The event-driven history recorder needs to look up a
// session's config_path immediately after SessionCreatedEvent.
func (a sessionAdapter) Get(path string) (ovpn.Session, error) {
	return a.SessionManager.Get(path)
}

func main() {
	// CLI dispatch first: subcommands return early without touching
	// the TUI scaffolding. Anything else falls through to run().
	if rc := cli.Dispatch(os.Args[1:], os.Stdout, os.Stderr); rc != -1 {
		os.Exit(rc)
	}
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}

// run sets up Service + watcher + sampler and hands control to the TUI.
// Returning errors instead of log.Fatal'ing inline so deferred Close
// calls actually fire — the original version was a gocritic
// `exitAfterDefer` hit because log.Fatal short-circuits process exit
// before the bus connection / overlay handle get released cleanly.
func run() error {
	conn, err := ovpn.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer conn.Close()

	overlayPath, err := overlay.DefaultPath()
	if err != nil {
		return fmt.Errorf("overlay path: %w", err)
	}
	ov, err := overlay.Open(overlayPath)
	if err != nil {
		return fmt.Errorf("open overlay: %w", err)
	}
	defer ov.Close()

	secs := secrets.New()
	prompter := tui.NewPrompter()

	svc := openapp.New(
		ovpn.NewConfigManager(conn),
		sessionAdapter{ovpn.NewSessionManager(conn)},
	)
	svc.SetStorage(ov, secs)
	svc.AttachBus(func() ovpn.Conn { return conn })
	svc.SetAuth(openapp.ChainAuth{Layers: []openapp.Auth{
		openapp.NewStoredCredentialsAuth(ov, secs),
		openapp.NewAutoTOTPAuth(ov, secs),
		openapp.NewPromptAuth(prompter),
	}})

	// History reconciliation at TUI startup:
	//   1. Sweep open rows whose session_path is not live (orphaned
	//      writes from previous runs that missed the destroy event).
	//   2. Make sure every currently live session has an open row —
	//      it may have been started by the desklet CLI or external
	//      tooling before the TUI launched.
	// Together these guarantee history reflects current bus state on
	// the very first render. TUI-only — short-lived CLI invocations
	// must not run any of this, otherwise they'd race their own
	// pending writes (e.g. `o3ui disconnect` would flip its own live
	// row to "lost" before it ran).
	if sessions, err := svc.ListSessions(); err == nil {
		live := make([]string, 0, len(sessions))
		for i := range sessions {
			live = append(live, sessions[i].Path)
		}
		_, _ = ov.SweepDanglingHistory(live)
	}
	svc.ReconcileLiveSessions()

	// Background sampler for live throughput charts on the Connected
	// screen. One per process is enough; iterates active sessions on
	// each tick.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sampler := openapp.NewSampler(svc, time.Second, 60)
	svc.AttachSampler(sampler)
	go sampler.Run(ctx)

	// Real-time D-Bus signal subscriber. Bridges the typed Event channel
	// into the bubbletea program via Run's events parameter.
	watcher := ovpn.NewWatcher(conn)
	go func() {
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("watcher: %v", err)
		}
	}()
	events := make(chan interface{}, 64)
	go func() {
		for ev := range watcher.Events() {
			// Fan out: Service finalises non-UI state (history rows
			// closed when a session dies without a user-initiated
			// Disconnect); the bubbletea program still gets every
			// event for screen routing.
			svc.HandleEvent(ev)
			events <- ev
		}
		close(events)
	}()

	if err := tui.Run(svc, prompter, events); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
