package ovpn

// Internal test: exercises translate() directly with synthetic dbus.Signal
// payloads. We don't spin up a Conn — the goal is to nail down the body
// shape mapping, not to integration-test godbus.

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

func TestTranslate_SessionManagerEvent_Created(t *testing.T) {
	sig := &dbus.Signal{
		Name: IfaceSessionsMgr + ".SessionManagerEvent",
		Path: PathSessions,
		Body: []interface{}{
			dbus.ObjectPath("/net/openvpn/v3/sessions/abc"),
			uint16(1), // created
			uint32(1000),
		},
	}
	ev := translate(sig)
	c, ok := ev.(SessionCreatedEvent)
	require.True(t, ok, "expected SessionCreatedEvent, got %T", ev)
	require.Equal(t, "/net/openvpn/v3/sessions/abc", c.Path)
	require.EqualValues(t, 1000, c.Owner)
}

func TestTranslate_SessionManagerEvent_Destroyed(t *testing.T) {
	sig := &dbus.Signal{
		Name: IfaceSessionsMgr + ".SessionManagerEvent",
		Body: []interface{}{
			dbus.ObjectPath("/net/openvpn/v3/sessions/abc"),
			uint16(2),
			uint32(1000),
		},
	}
	_, ok := translate(sig).(SessionDestroyedEvent)
	require.True(t, ok)
}

func TestTranslate_StatusChange(t *testing.T) {
	sig := &dbus.Signal{
		Name: IfaceSession + ".StatusChange",
		Path: "/net/openvpn/v3/sessions/abc",
		Body: []interface{}{uint32(2), uint32(7), "connected"},
	}
	ev := translate(sig)
	sc, ok := ev.(StatusChangeEvent)
	require.True(t, ok, "got %T", ev)
	require.Equal(t, "/net/openvpn/v3/sessions/abc", sc.Path)
	require.True(t, sc.Status.IsConnected())
	require.Equal(t, "connected", sc.Status.Message)
}

func TestTranslate_AttentionRequired(t *testing.T) {
	sig := &dbus.Signal{
		Name: IfaceSession + ".AttentionRequired",
		Path: "/net/openvpn/v3/sessions/abc",
		Body: []interface{}{uint32(1), uint32(2), "static_challenge"},
	}
	ev := translate(sig)
	ar, ok := ev.(AttentionRequiredEvent)
	require.True(t, ok)
	require.Equal(t, "static_challenge", ar.Message)
	require.EqualValues(t, 1, ar.Major)
	require.EqualValues(t, 2, ar.Minor)
}

func TestTranslate_ConfigurationManagerEvent(t *testing.T) {
	created := &dbus.Signal{
		Name: IfaceConfigurationMgr + ".ConfigurationManagerEvent",
		Body: []interface{}{
			dbus.ObjectPath("/net/openvpn/v3/configuration/x"),
			uint16(1),
			uint32(1000),
		},
	}
	c, ok := translate(created).(ConfigCreatedEvent)
	require.True(t, ok)
	require.Equal(t, "/net/openvpn/v3/configuration/x", c.Path)

	destroyed := &dbus.Signal{
		Name: IfaceConfigurationMgr + ".ConfigurationManagerEvent",
		Body: []interface{}{
			dbus.ObjectPath("/net/openvpn/v3/configuration/x"),
			uint16(2),
			uint32(1000),
		},
	}
	_, ok = translate(destroyed).(ConfigDestroyedEvent)
	require.True(t, ok)
}

func TestTranslate_UnknownSignalReturnsNil(t *testing.T) {
	sig := &dbus.Signal{
		Name: "org.something.else.Bored",
		Body: []interface{}{uint32(0)},
	}
	require.Nil(t, translate(sig))
}

func TestTranslate_MalformedBodyReturnsNil(t *testing.T) {
	// Right name, wrong arity — must return nil, not panic.
	sig := &dbus.Signal{
		Name: IfaceSession + ".StatusChange",
		Body: []interface{}{uint32(2)}, // missing minor + message
	}
	require.Nil(t, translate(sig))
}
