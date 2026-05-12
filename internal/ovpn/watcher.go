package ovpn

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Event is the discriminated-union type emitted by Watcher. Consumers
// switch on the concrete type rather than using flags or kind fields,
// which keeps the per-event payloads strongly typed.
type Event interface{ event() }

// SessionCreatedEvent fires when openvpn3 registers a new session
// object on the bus (someone called NewTunnel).
type SessionCreatedEvent struct {
	Path  string
	Owner uint32
}

// SessionDestroyedEvent fires when openvpn3 removes a session — typically
// after Disconnect, sometimes a couple of seconds later.
type SessionDestroyedEvent struct {
	Path  string
	Owner uint32
}

// StatusChangeEvent fires on every status transition of a session
// (connecting → connected → disconnected, plus reconnect attempts and
// auth state changes). Path is the session that changed.
type StatusChangeEvent struct {
	Path   string
	Status Status
}

// AttentionRequiredEvent fires when openvpn3 needs user input (creds,
// OTP). Lets the UI react instantly instead of polling
// UserInputQueueGetTypeGroup.
type AttentionRequiredEvent struct {
	Path    string
	Major   uint32
	Minor   uint32
	Message string
}

// ConfigCreatedEvent fires when a configuration is imported (UI may not
// be the importer — someone using `openvpn3 config-import` triggers it
// too, and we want to reflect that immediately).
type ConfigCreatedEvent struct{ Path string }

// ConfigDestroyedEvent fires when a configuration is removed.
type ConfigDestroyedEvent struct{ Path string }

func (SessionCreatedEvent) event()    {}
func (SessionDestroyedEvent) event()  {}
func (StatusChangeEvent) event()      {}
func (AttentionRequiredEvent) event() {}
func (ConfigCreatedEvent) event()     {}
func (ConfigDestroyedEvent) event()   {}

// openvpn3 manager-event type codes (from sessions/mapping.go in the
// reference Go prototype): 1 = created, 2 = destroyed.
const (
	mgrEventCreated   uint16 = 1
	mgrEventDestroyed uint16 = 2
)

// Watcher subscribes to openvpn3 signals on the system bus and re-emits
// them as typed Events. One Watcher per process is sufficient.
type Watcher struct {
	conn Conn
	out  chan Event
}

// NewWatcher creates an unstarted watcher with a buffered output channel.
// Buffer = 64 so a brief stall in the consumer doesn't deadlock D-Bus.
func NewWatcher(conn Conn) *Watcher {
	return &Watcher{conn: conn, out: make(chan Event, 64)}
}

// Events is the channel consumers read. Closed when Run returns.
func (w *Watcher) Events() <-chan Event { return w.out }

// Run subscribes to the relevant signals and pumps them into Events
// until ctx is cancelled. Blocks; typically launched in its own goroutine.
//
// Cleanup ordering matters: drop the signal channel from godbus *before*
// closing `out`, otherwise godbus's dispatcher can race a final write
// against our close(out) and panic with "send on closed channel".
// Same logic for AddMatchSignal subscriptions — leaving them around
// after Run returns keeps godbus dispatching into a dead channel.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.subscribe(); err != nil {
		return err
	}
	raw := make(chan *dbus.Signal, 128)
	w.conn.Signal(raw)

	defer func() {
		w.conn.RemoveSignal(raw)
		w.unsubscribe()
		close(w.out)
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig := <-raw:
			if sig == nil {
				continue
			}
			if ev := translate(sig); ev != nil {
				// Drop if the consumer is hopelessly behind — we'd rather
				// lose a single status update than wedge openvpn3 itself
				// behind our render loop.
				select {
				case w.out <- ev:
				default:
				}
			}
		}
	}
}

// matchSpecs is the single source of truth for the signals we listen
// to — both subscribe and unsubscribe enumerate it so they always
// stay in sync. (Dropping a match after a subscribe failure isn't
// strictly required since godbus is idempotent, but it keeps the
// dispatch table from growing across reconnects.)
func matchSpecs() []struct {
	opts []dbus.MatchOption
	desc string
} {
	return []struct {
		opts []dbus.MatchOption
		desc string
	}{
		{
			[]dbus.MatchOption{
				dbus.WithMatchInterface(IfaceSessionsMgr),
				dbus.WithMatchMember("SessionManagerEvent"),
			},
			"SessionManagerEvent",
		},
		{
			[]dbus.MatchOption{
				dbus.WithMatchInterface(IfaceSession),
				dbus.WithMatchMember("StatusChange"),
			},
			"StatusChange",
		},
		{
			[]dbus.MatchOption{
				dbus.WithMatchInterface(IfaceSession),
				dbus.WithMatchMember("AttentionRequired"),
			},
			"AttentionRequired",
		},
		{
			[]dbus.MatchOption{
				dbus.WithMatchInterface(IfaceConfigurationMgr),
				dbus.WithMatchMember("ConfigurationManagerEvent"),
			},
			"ConfigurationManagerEvent",
		},
	}
}

func (w *Watcher) subscribe() error {
	for _, m := range matchSpecs() {
		if err := w.conn.AddMatchSignal(m.opts...); err != nil {
			return fmt.Errorf("watcher subscribe %s: %w", m.desc, err)
		}
	}
	return nil
}

// unsubscribe drops every match that subscribe registered. Errors are
// swallowed — by the time Run is exiting, the connection may already
// be closing too, and there's nothing useful for the caller to do.
func (w *Watcher) unsubscribe() {
	for _, m := range matchSpecs() {
		_ = w.conn.RemoveMatchSignal(m.opts...)
	}
}

// translate converts a raw D-Bus signal into a typed Event. Returns nil
// when the signal isn't one we care about or the body shape is wrong —
// silently dropping malformed payloads is the right call here, the
// alternative is crashing on a future openvpn3 schema change.
func translate(sig *dbus.Signal) Event {
	switch sig.Name {
	case IfaceSessionsMgr + ".SessionManagerEvent":
		// body: (o path, q event_type, u owner)
		if len(sig.Body) < 3 {
			return nil
		}
		path, _ := sig.Body[0].(dbus.ObjectPath)
		etype, _ := sig.Body[1].(uint16)
		owner, _ := sig.Body[2].(uint32)
		switch etype {
		case mgrEventCreated:
			return SessionCreatedEvent{Path: string(path), Owner: owner}
		case mgrEventDestroyed:
			return SessionDestroyedEvent{Path: string(path), Owner: owner}
		}
	case IfaceSession + ".StatusChange":
		// body: (u code_major, u code_minor, s message)
		if len(sig.Body) < 3 {
			return nil
		}
		major, _ := sig.Body[0].(uint32)
		minor, _ := sig.Body[1].(uint32)
		msg, _ := sig.Body[2].(string)
		return StatusChangeEvent{
			Path:   string(sig.Path),
			Status: Status{Major: major, Minor: minor, Message: msg},
		}
	case IfaceSession + ".AttentionRequired":
		// body: (u code_major, u code_minor, s message)
		if len(sig.Body) < 3 {
			return nil
		}
		major, _ := sig.Body[0].(uint32)
		minor, _ := sig.Body[1].(uint32)
		msg, _ := sig.Body[2].(string)
		return AttentionRequiredEvent{
			Path: string(sig.Path), Major: major, Minor: minor, Message: msg,
		}
	case IfaceConfigurationMgr + ".ConfigurationManagerEvent":
		if len(sig.Body) < 3 {
			return nil
		}
		path, _ := sig.Body[0].(dbus.ObjectPath)
		etype, _ := sig.Body[1].(uint16)
		switch etype {
		case mgrEventCreated:
			return ConfigCreatedEvent{Path: string(path)}
		case mgrEventDestroyed:
			return ConfigDestroyedEvent{Path: string(path)}
		}
	}
	return nil
}
