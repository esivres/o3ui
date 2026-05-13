package cli

// Pipe-API: a long-running stdin/stdout JSON-lines RPC server. Solves
// the "desklet spawns o3ui every tick" problem by giving the desklet
// (or any other parent) one persistent backend that owns a single
// D-Bus connection and Service for its whole lifetime.
//
// Wire format (newline-delimited, one JSON document per line):
//
//   →  {"id":N,"method":"<name>","args":{...}}      // request
//   ←  {"id":N,"result":<value>}                    // ok
//   ←  {"id":N,"error":"<message>"}                 // failure
//   ←  {"event":"<name>","data":{...}}              // push (v1: none)
//
// The server exits on EOF on stdin — when the parent dies, kernel
// closes our stdin, the scan loop returns, deferred cleanup runs.
//
// Methods mirror the existing `o3ui status/list/connect/disconnect`
// subcommands; results are the same JSON shape that those return with
// `--json`, so the desklet can drop runAsync(subprocess) for
// client.call(method) without changing any of its rendering code.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	openapp "github.com/esivres/openvpn3ui/internal/app"
	"github.com/esivres/openvpn3ui/internal/ovpn"
)

type pipeRequest struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type pipeResponse struct {
	ID     int         `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// runPipeAPI is the entry point for `o3ui pipe-api`. Holds one Service
// for the process lifetime and serves RPCs until stdin closes.
func runPipeAPI(_ []string, stdout, stderr io.Writer) int {
	svc, cleanup, err := buildService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()

	// One writer mutex — every goroutine that produces a line must
	// hold it for the full encode+write so two responses don't get
	// interleaved on stdout.
	var outMu sync.Mutex
	write := func(v interface{}) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		outMu.Lock()
		defer outMu.Unlock()
		_, _ = stdout.Write(append(b, '\n'))
	}

	sc := bufio.NewScanner(os.Stdin)
	// Default Scanner buffer is 64KB; bump to 1MB so we don't choke
	// on a future fat request (config import payload, etc.).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var wg sync.WaitGroup
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req pipeRequest
		if err := json.Unmarshal(line, &req); err != nil {
			write(pipeResponse{Error: "bad json: " + err.Error()})
			continue
		}
		// Each request runs in its own goroutine so a slow Connect
		// (≤60s) doesn't block subsequent Status polls. The shared
		// Service is safe under concurrent reads; concurrent writes
		// (Connect/Disconnect) are not expected from the desklet
		// flow but would still serialise on openvpn3-bus itself.
		wg.Add(1)
		go func(r pipeRequest) {
			defer wg.Done()
			handlePipeRequest(svc, r, write)
		}(req)
	}
	// Drain pending requests before tearing down Service. Otherwise
	// they'd race the deferred conn.Close() and produce confusing
	// "use of closed D-Bus connection" errors in logs.
	wg.Wait()
	return 0
}

func handlePipeRequest(svc *openapp.Service, req pipeRequest, write func(interface{})) {
	switch req.Method {
	case "status":
		rep, err := buildStatusReport(svc)
		if err != nil {
			write(pipeResponse{ID: req.ID, Error: err.Error()})
			return
		}
		write(pipeResponse{ID: req.ID, Result: rep})

	case "list":
		out, err := buildListReport(svc)
		if err != nil {
			write(pipeResponse{ID: req.ID, Error: err.Error()})
			return
		}
		write(pipeResponse{ID: req.ID, Result: out})

	case "connect":
		var a struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if a.Target == "" {
			write(pipeResponse{ID: req.ID, Error: "missing target"})
			return
		}
		cfg, err := resolveProfile(svc, a.Target)
		if err != nil {
			write(pipeResponse{ID: req.ID, Error: err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		sessionPath, err := svc.Connect(ctx, cfg.Path)
		if err != nil {
			msg := err.Error()
			if ctx.Err() != nil {
				msg = "connect timed out after 60s"
			}
			write(pipeResponse{ID: req.ID, Error: msg})
			return
		}
		write(pipeResponse{ID: req.ID, Result: map[string]string{
			"profile":      cfg.Name,
			"config_path":  cfg.Path,
			"session_path": sessionPath,
		}})

	case "disconnect":
		var a struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Args, &a)
		sessions, err := svc.ActiveSessions()
		if err != nil {
			write(pipeResponse{ID: req.ID, Error: err.Error()})
			return
		}
		if len(sessions) == 0 {
			write(pipeResponse{ID: req.ID, Result: map[string]string{"message": "no active session"}})
			return
		}
		var target *ovpn.Session
		if a.Target != "" {
			ref := strings.ToLower(a.Target)
			for i := range sessions {
				if sessions[i].Path == a.Target ||
					strings.Contains(strings.ToLower(sessions[i].ConfigName), ref) {
					target = &sessions[i]
					break
				}
			}
			if target == nil {
				write(pipeResponse{ID: req.ID, Error: "no active session matches " + a.Target})
				return
			}
		} else {
			target = &sessions[0]
		}
		if err := svc.Disconnect(target.Path); err != nil {
			write(pipeResponse{ID: req.ID, Error: err.Error()})
			return
		}
		write(pipeResponse{ID: req.ID, Result: map[string]string{
			"profile":      target.ConfigName,
			"session_path": target.Path,
		}})

	case "ping":
		// Cheap liveness probe for the desklet's watchdog.
		write(pipeResponse{ID: req.ID, Result: "pong"})

	default:
		write(pipeResponse{ID: req.ID, Error: "unknown method: " + req.Method})
	}
}

// listEntry is the same shape `o3ui list --json` produces. Extracted
// here so the pipe-api can reuse it without re-encoding via stdout
// capture.
type listEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Country     string `json:"country,omitempty"`
	Favorite    bool   `json:"favorite"`
	AutoConnect bool   `json:"auto_connect"`
}

func buildListReport(svc *openapp.Service) ([]listEntry, error) {
	cfgs, err := svc.ListConfigs()
	if err != nil {
		return nil, err
	}
	out := make([]listEntry, 0, len(cfgs))
	for _, c := range cfgs {
		e := listEntry{Name: c.Name, Path: c.Path}
		if o, ok := svc.GetOverlay(c.Path); ok {
			e.Country = o.CountryCode
			e.Favorite = o.Favorite
			e.AutoConnect = o.AutoConnect
		}
		out = append(out, e)
	}
	return out, nil
}
