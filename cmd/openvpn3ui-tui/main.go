// openvpn3ui-tui is the Bubble Tea / lipgloss terminal UI for openvpn3.
// The Fyne GUI binary stays as cmd/openvpn3ui — this is the v1 ship target.
package main

import (
	"context"
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

func main() {
	// CLI dispatch: any subcommand (status, list, connect, disconnect,
	// desklet) is handled by internal/cli without spinning up the TUI.
	// Returns -1 when no subcommand matched, in which case we fall
	// through to the interactive TUI below.
	if rc := cli.Dispatch(os.Args[1:], os.Stdout, os.Stderr); rc != -1 {
		os.Exit(rc)
	}

	conn, err := ovpn.ConnectSystemBus()
	if err != nil {
		log.Fatalf("connect system bus: %v", err)
	}
	defer conn.Close()

	overlayPath, err := overlay.DefaultPath()
	if err != nil {
		log.Fatalf("overlay path: %v", err)
	}
	ov, err := overlay.Open(overlayPath)
	if err != nil {
		log.Fatalf("open overlay: %v", err)
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
		if err := watcher.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("watcher: %v", err)
		}
	}()
	events := make(chan interface{}, 64)
	go func() {
		for ev := range watcher.Events() {
			events <- ev
		}
		close(events)
	}()

	if err := tui.Run(svc, prompter, events); err != nil {
		log.Fatalf("tui: %v", err)
	}
}
