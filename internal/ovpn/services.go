package ovpn

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BackendService describes one openvpn3 D-Bus service. The Settings screen
// renders these in a table.
type BackendService struct {
	Name    string    // bus name, e.g. "net.openvpn.v3.configuration"
	State   string    // "running" or "activatable"
	PID     uint32    // 0 when not running
	Started time.Time // zero when not running or unknown
}

// Uptime is the convenience accessor used by the UI; returns 0 if Started
// is the zero value.
func (s BackendService) Uptime() time.Duration {
	if s.Started.IsZero() {
		return 0
	}
	return time.Since(s.Started)
}

// ListBackendServices enumerates every `net.openvpn.v3.*` bus name on the
// system bus, marks each as running or activatable, and looks up PID +
// process start time for the running ones. Best-effort: a process that
// disappears between ListNames and the per-name lookups simply ends up
// with State="running", PID=0, Started=zero — never an error.
func ListBackendServices(c Conn) ([]BackendService, error) {
	bus := c.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")

	var live []string
	if err := bus.Call("org.freedesktop.DBus.ListNames", 0).Store(&live); err != nil {
		return nil, fmt.Errorf("ListNames: %w", err)
	}
	var activatable []string
	if err := bus.Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil {
		return nil, fmt.Errorf("ListActivatableNames: %w", err)
	}

	seen := map[string]struct{}{}
	addOpenVPN := func(name string, state string, out *[]BackendService) {
		if !strings.HasPrefix(name, "net.openvpn.v3.") {
			return
		}
		// Skip per-backend instance names like "net.openvpn.v3.backends.beXXX".
		// We surface only the parent activatable names; instance ones
		// crowd the table without telling the user anything new.
		short := strings.TrimPrefix(name, "net.openvpn.v3.")
		if strings.Contains(short, ".") {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}

		svc := BackendService{Name: name, State: state}
		if state == "running" {
			if pid, ok := connectionPID(c, name); ok {
				svc.PID = pid
				if started, ok := procStarted(int(pid)); ok {
					svc.Started = started
				}
			}
		}
		*out = append(*out, svc)
	}

	var out []BackendService
	for _, n := range live {
		addOpenVPN(n, "running", &out)
	}
	for _, n := range activatable {
		addOpenVPN(n, "activatable", &out)
	}
	return out, nil
}

func connectionPID(c Conn, busName string) (uint32, bool) {
	bus := c.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	var pid uint32
	if err := bus.Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, busName).Store(&pid); err != nil {
		return 0, false
	}
	return pid, true
}

// procStarted returns when a Linux process was launched, derived from
// /proc/<pid>/stat field 22 (starttime in clock ticks since boot) and
// /proc/uptime. Returns ok=false on any I/O or parse error so callers can
// silently skip.
func procStarted(pid int) (time.Time, bool) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, false
	}
	// `comm` (field 2) may contain spaces and parens; isolate it first.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 || end+2 > len(stat) {
		return time.Time{}, false
	}
	rest := strings.Fields(string(stat[end+2:]))
	// After comm we are at field 3. starttime is field 22 → index 19 in `rest`.
	if len(rest) < 20 {
		return time.Time{}, false
	}
	startTicks, err := strconv.ParseInt(rest[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	// /proc/uptime: "secondsSinceBoot idleSeconds" — float seconds.
	upRaw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}, false
	}
	upFields := strings.Fields(string(upRaw))
	if len(upFields) < 1 {
		return time.Time{}, false
	}
	upSec, err := strconv.ParseFloat(upFields[0], 64)
	if err != nil {
		return time.Time{}, false
	}

	const userHZ = 100 // typical Linux value
	procUpSec := upSec - float64(startTicks)/userHZ
	if procUpSec < 0 {
		procUpSec = 0
	}
	return time.Now().Add(-time.Duration(procUpSec * float64(time.Second))), true
}
