/*
 * OVPN3 Cinnamon desklet
 *
 * A thin status/control widget for openvpn3, driven by the o3ui CLI.
 * Holds one long-lived `o3ui pipe-api` child for the desklet lifetime
 * and speaks JSON-lines RPC over stdin/stdout. The previous fork-per-
 * tick model leaked zombies (DO_NOT_REAP_CHILD without a child_watch);
 * this version spawns once, child_watch reaps on respawn, and the
 * watchdog restarts the backend with exponential backoff if it dies.
 *
 * The poll loop calls `status` every 2s and re-renders the matching
 * state:
 *
 *   disconnected → big green Connect (+ profile picker on click)
 *   connecting   → amber pulse, gradient progress, handshake steps
 *   connected    → ↓/↑ rates, ping, uptime, sparkline, Disconnect
 *   failed       → red dot, error reason, Retry
 *
 * Settings (configurable via the Cinnamon desklet panel):
 *   - profile: name of the openvpn3 profile to drive (free-text)
 *   - compact: shrink to the 240px minimal layout
 *   - cli_path: override the o3ui binary path (defaults to "o3ui" on $PATH)
 *
 * The desklet talks to openvpn3 only through the CLI — no D-Bus on
 * the JS side. That keeps auth/TOTP/keyring code in one place (Go).
 */

const Desklet = imports.ui.desklet;
const St = imports.gi.St;
const Clutter = imports.gi.Clutter;
const GLib = imports.gi.GLib;
const Gio = imports.gi.Gio;
const Settings = imports.ui.settings;
const Tweener = imports.ui.tweener;
const Util = imports.misc.util;
const Mainloop = imports.mainloop;
const Lang = imports.lang;

const UUID = "o3ui@esivres";
const POLL_INTERVAL_SECONDS = 2;

// ── small helpers ────────────────────────────────────────────────────

function humanBytes(n) {
    if (!n || n < 0) return "0 B";
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    if (n < 1024 * 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + " MB";
    return (n / (1024 * 1024 * 1024)).toFixed(2) + " GB";
}

function humanDur(sec) {
    sec = sec || 0;
    let h = Math.floor(sec / 3600);
    let m = Math.floor((sec % 3600) / 60);
    let s = sec % 60;
    if (h > 0) {
        return _pad(h) + ":" + _pad(m) + ":" + _pad(s);
    }
    return _pad(m) + ":" + _pad(s);
}

function _pad(n) {
    return (n < 10 ? "0" : "") + n;
}

// RpcClient maintains a single long-lived `o3ui pipe-api` child and
// speaks JSON-lines with it over stdin/stdout. It replaces the old
// fork-per-tick model that leaked one zombie every poll (~7000/hour at
// 2 Hz). Lifecycle:
//
//   start()      spawn child, wire stdin/stdout streams, install
//                child_watch to reap on exit, kick off the read loop.
//   call()       send a request, register the callback in `pending`,
//                resolve when the matching {id,result|error} arrives.
//   _onExit()    child died → fail every pending callback so the UI
//                doesn't hang forever, then schedule respawn with
//                exponential backoff (1s, 2s, 4s, 8s, capped 30s).
//   stop()       deliberate shutdown (on_desklet_removed): cancel the
//                respawn timer, close stdin (child sees EOF and exits
//                cleanly), let child_watch reap.
//
// All IO is async on the main Mainloop — we never block GJS. Writes are
// done via Gio.DataOutputStream.put_string which is sync but only on a
// pipe with empty kernel buffer space; our payloads are ~100 bytes so
// this is effectively non-blocking.
const RPC_BACKOFF_MS = [1000, 2000, 4000, 8000, 16000, 30000];

function RpcClient(argv) {
    this.argv = argv;
    this.pid = null;
    this.stdinStream = null;
    this.stdoutStream = null;
    this.stdoutReader = null;
    this.pending = {};
    this.nextId = 1;
    this.failures = 0;
    this.alive = false;
    this.respawnTimerId = 0;
    this.stopped = false;
    this.onEvent = null;
    this._start();
}

RpcClient.prototype._start = function () {
    if (this.stopped) return;
    let res;
    try {
        res = GLib.spawn_async_with_pipes(
            null, this.argv, null,
            GLib.SpawnFlags.SEARCH_PATH | GLib.SpawnFlags.DO_NOT_REAP_CHILD,
            null
        );
    } catch (e) {
        this._scheduleRespawn();
        return;
    }

    let stdinFd, stdoutFd, stderrFd;
    if (res.length === 5) {
        if (!res[0]) { this._scheduleRespawn(); return; }
        this.pid = res[1]; stdinFd = res[2]; stdoutFd = res[3]; stderrFd = res[4];
    } else {
        this.pid = res[0]; stdinFd = res[1]; stdoutFd = res[2]; stderrFd = res[3];
    }
    // stderr is unused by pipe-api; close immediately so it doesn't
    // pile up FDs over respawns.
    if (stderrFd !== null) { try { GLib.close(stderrFd); } catch (e) {} }

    this.stdinStream = new Gio.DataOutputStream({
        base_stream: new Gio.UnixOutputStream({ fd: stdinFd, close_fd: true }),
    });
    this.stdoutStream = new Gio.UnixInputStream({ fd: stdoutFd, close_fd: true });
    this.stdoutReader = new Gio.DataInputStream({ base_stream: this.stdoutStream });

    // child_watch_add internally calls waitpid() when the child exits;
    // spawn_close_pid in the callback frees the PID slot. Without this
    // every child sits as <defunct> until cinnamon itself dies.
    let self = this;
    GLib.child_watch_add(GLib.PRIORITY_DEFAULT, this.pid, function (p, _status) {
        try { GLib.spawn_close_pid(p); } catch (e) {}
        self._onExit();
    });

    this.alive = true;
    this._readNext();
};

RpcClient.prototype._readNext = function () {
    let self = this;
    if (!this.stdoutReader || !this.alive) return;
    this.stdoutReader.read_line_async(GLib.PRIORITY_DEFAULT, null, function (s, r) {
        if (!self.alive) return;
        let line;
        try {
            let res = s.read_line_finish_utf8(r);
            line = res[0];
        } catch (e) {
            self._onExit();
            return;
        }
        if (line === null) {
            // EOF — child closed stdout (probably crashed). _onExit
            // also fires from child_watch but is idempotent.
            self._onExit();
            return;
        }
        try {
            let msg = JSON.parse(line);
            if (typeof msg.id === "number" && self.pending[msg.id]) {
                let cb = self.pending[msg.id];
                delete self.pending[msg.id];
                cb(msg.result === undefined ? null : msg.result,
                   msg.error || null);
            } else if (msg.event && self.onEvent) {
                self.onEvent(msg);
            }
        } catch (e) { /* swallow malformed line — child may emit log noise on stderr-merged setups */ }
        self._readNext();
    });
};

RpcClient.prototype._onExit = function () {
    if (!this.alive) return;
    this.alive = false;
    // Fail every pending callback so the UI doesn't sit on a spinner.
    let pending = this.pending;
    this.pending = {};
    for (let id in pending) {
        try { pending[id](null, "backend exited"); } catch (e) {}
    }
    if (this.stdoutReader) { try { this.stdoutReader.close(null); } catch (e) {} }
    if (this.stdoutStream) { try { this.stdoutStream.close(null); } catch (e) {} }
    if (this.stdinStream)  { try { this.stdinStream.close(null);  } catch (e) {} }
    this.stdoutReader = null;
    this.stdoutStream = null;
    this.stdinStream = null;
    this.pid = null;
    this.failures++;
    this._scheduleRespawn();
};

RpcClient.prototype._scheduleRespawn = function () {
    if (this.stopped) return;
    let idx = Math.min(this.failures, RPC_BACKOFF_MS.length - 1);
    let delay = RPC_BACKOFF_MS[idx];
    let self = this;
    this.respawnTimerId = Mainloop.timeout_add(delay, function () {
        self.respawnTimerId = 0;
        self._start();
        return false;
    });
};

RpcClient.prototype.call = function (method, args, cb) {
    if (this.stopped) { cb(null, "client stopped"); return; }
    if (!this.alive)  { cb(null, "backend not ready"); return; }
    let id = this.nextId++;
    let self = this;
    this.pending[id] = function (result, err) {
        if (!err) self.failures = 0; // a successful round-trip resets the backoff
        cb(result, err);
    };
    let payload = JSON.stringify({ id: id, method: method, args: args || {} }) + "\n";
    try {
        this.stdinStream.put_string(payload, null);
        this.stdinStream.flush(null);
    } catch (e) {
        delete this.pending[id];
        cb(null, "write failed: " + e);
        this._onExit();
    }
};

RpcClient.prototype.stop = function () {
    this.stopped = true;
    if (this.respawnTimerId) {
        Mainloop.source_remove(this.respawnTimerId);
        this.respawnTimerId = 0;
    }
    // Closing stdin gives the child a clean EOF — pipe-api's scanner
    // loop returns and the process exits 0. child_watch_add reaps.
    if (this.stdinStream) { try { this.stdinStream.close(null); } catch (e) {} }
    this.stdinStream = null;
};

// ── main desklet ─────────────────────────────────────────────────────

function MyDesklet(metadata, desklet_id) {
    this._init(metadata, desklet_id);
}

MyDesklet.prototype = {
    __proto__: Desklet.Desklet.prototype,

    _init: function (metadata, desklet_id) {
        Desklet.Desklet.prototype._init.call(this, metadata, desklet_id);

        this.settings = new Settings.DeskletSettings(this, UUID, desklet_id);
        this.settings.bindProperty(Settings.BindingDirection.IN, "profile",
            "profile", this._onSettingsChanged, null);
        this.settings.bindProperty(Settings.BindingDirection.IN, "compact",
            "compact", this._onSettingsChanged, null);
        this.settings.bindProperty(Settings.BindingDirection.IN, "cli_path",
            "cli_path", this._onSettingsChanged, null);

        // Latest status report from the CLI; null until the first poll.
        this._status = null;
        this._pollId = null;
        this._sparkBytesIn = [];
        this._sparkBytesOut = [];

        // Profile-picker overlay state. When `_picking` is true, the
        // status states yield to the picker view; `_profiles` is the
        // last `o3ui list --json` snapshot, refetched on every open.
        // `_selected` is the user's explicit pick — overrides both
        // the configured setting and the CLI-supplied default until
        // the desklet is reloaded.
        this._picking = false;
        this._profiles = [];
        this._selected = "";

        // _currentKind tracks which view layout is currently materialised
        // in `this._root`. On a poll tick we only destroy+rebuild when
        // the kind changes (state transition, compact toggle, picker
        // open/close). Otherwise we patch existing labels in place via
        // _refs — that keeps the rate / uptime / sparkline updates
        // flicker-free, which is critical at the 2 Hz poll rate.
        this._currentKind = "";
        this._refs = {};

        // _anims is the list of actors with active Tweener animations
        // (the connecting dot pulse, the progress fill slide). Cleared
        // on every kind change so we don't leave dangling tweens that
        // keep animating after a state transition.
        this._anims = [];

        this._root = new St.BoxLayout({
            vertical: true,
            style_class: "o3ui-desklet",
        });
        this.setContent(this._root);
        this._render();
        // Start the long-lived backend before kicking off the poll
        // loop — the very first poll tick is already running through it.
        this._rpc = new RpcClient([this._cliBin(), "pipe-api"]);
        this._startPolling();
    },

    on_desklet_removed: function () {
        if (this._pollId) {
            Mainloop.source_remove(this._pollId);
            this._pollId = null;
        }
        if (this._rpc) {
            this._rpc.stop();
            this._rpc = null;
        }
        this._stopAnims();
    },

    _stopAnims: function () {
        for (let i = 0; i < this._anims.length; i++) {
            try { Tweener.removeTweens(this._anims[i]); } catch (e) {}
        }
        this._anims = [];
    },

    // _startPulse drives an infinite opacity heartbeat on the given
    // actor (typically the connecting dot). Self-chaining onComplete
    // — Tweener has no built-in loop. Stopped via removeTweens when
    // the kind transitions out.
    _startPulse: function (actor) {
        if (!actor) return;
        this._anims.push(actor);
        let self = this;
        let down = function () {
            Tweener.addTween(actor, {
                opacity: 90, time: 0.8, transition: "easeInOutSine",
                onComplete: up,
            });
        };
        let up = function () {
            Tweener.addTween(actor, {
                opacity: 255, time: 0.8, transition: "easeInOutSine",
                onComplete: down,
            });
        };
        actor.opacity = 255;
        down();
    },

    // _startProgressSlide loops a translation across the track. The
    // pill begins off the left edge, runs past the right, then jumps
    // back to start — same effect as the design's CSS @keyframes.
    _startProgressSlide: function (actor, startX, endX) {
        if (!actor) return;
        this._anims.push(actor);
        let self = this;
        let cycle = function () {
            actor.set_x(startX);
            Tweener.addTween(actor, {
                x: endX,
                time: 1.6, transition: "easeInOutSine",
                onComplete: cycle,
            });
        };
        cycle();
    },

    _onSettingsChanged: function () {
        if (this._root) {
            this._root.set_style_class_name(
                this.compact ? "o3ui-desklet compact" : "o3ui-desklet"
            );
        }
        this._render();
    },

    // _cliBin resolves the o3ui binary. Order of preference:
    //   1. The cli_path setting, if filled in via Configure.
    //   2. Cached probe result from this session.
    //   3. First existing candidate among the usual install locations.
    //   4. Fallback "o3ui" — relies on $PATH, which Cinnamon's session
    //      usually does not include ~/.local/bin in.
    // The probe runs lazily on first call, then memoises. This way
    // a desklet installed under ~/.local/bin works out of the box
    // even if the user never touches Configure.
    _cliBin: function () {
        if (this.cli_path && this.cli_path.length > 0) return this.cli_path;
        if (this._probedBin) return this._probedBin;
        let home = GLib.get_home_dir();
        let candidates = [
            home + "/.local/bin/o3ui",
            "/usr/local/bin/o3ui",
            "/usr/bin/o3ui",
        ];
        for (let i = 0; i < candidates.length; i++) {
            try {
                let f = Gio.File.new_for_path(candidates[i]);
                if (f.query_exists(null)) {
                    this._probedBin = candidates[i];
                    return candidates[i];
                }
            } catch (e) { /* keep probing */ }
        }
        return "o3ui";
    },

    _startPolling: function () {
        this._poll();
        this._pollId = Mainloop.timeout_add_seconds(
            POLL_INTERVAL_SECONDS, Lang.bind(this, function () {
                this._poll();
                return true; // repeat
            })
        );
    },

    _poll: function () {
        this._rpc.call("status", {}, Lang.bind(this, function (result, err) {
            if (err) {
                this._status = { state: "error", message: err };
                this._render();
                return;
            }
            this._status = result || {};
            // Pull spark series from the backend cache; the backend is
            // the sample-holder for cross-tick deltas.
            if (this._status.spark_in) this._sparkBytesIn = this._status.spark_in;
            if (this._status.spark_out) this._sparkBytesOut = this._status.spark_out;
            this._render();
        }));
    },

    // _render rebuilds the desklet body from scratch on every state
    // change. Cheap with this surface area; lets us treat each state as
    // a pure function of the status JSON without diffing.
    // _kindOf maps the current snapshot to a view-id. Two ticks that
    // produce the same kind reuse the existing widget tree; different
    // kinds trigger a full rebuild. Keep this pure / cheap — it runs
    // every poll.
    _kindOf: function (s) {
        if (this._picking) return "picker";
        if (!s) return "loading";
        if (s.state === "connected") return this.compact ? "connected_c" : "connected";
        return s.state || "disconnected";
    },

    // _patch updates the labels that change per poll, without ever
    // destroying widgets. Only the two connected kinds have animated
    // values worth patching; for the rest a tick produces no visible
    // change anyway and we can let the no-op slide through.
    _patch: function (kind, s) {
        if (kind === "connected" || kind === "connected_c") {
            this._patchStatusRowForConnected(s);
            if (kind === "connected") this._patchConnectedFull(s);
            else this._patchConnectedCompact(s);
            // Refresh the sparkline canvas from the freshly-poked
            // series. The closure reads `this._sparkBytes*` live, so
            // queue_repaint is the only thing we need.
            if (this._refs.sparkArea) {
                let peak = 0;
                for (let i = 0; i < this._sparkBytesIn.length; i++) {
                    if (this._sparkBytesIn[i] > peak) peak = this._sparkBytesIn[i];
                }
                if (this._refs.sparkPeak) {
                    this._refs.sparkPeak.set_text("peak " + humanRate(peak));
                }
                this._refs.sparkArea.queue_repaint();
            }
        }
    },

    _patchStatusRowForConnected: function (s) {
        let parts = [];
        if (s.country) parts.push(s.country);
        if (s.uptime_sec) parts.push(humanDur(s.uptime_sec));
        if (this._refs.sub) {
            this._refs.sub.set_text(parts.join(" · ") || (s.profile || ""));
        }
    },

    _patchConnectedFull: function (s) {
        if (this._refs.down) {
            this._refs.down.val.set_text(humanRate(s.rate_in));
            if (this._refs.down.sub) {
                this._refs.down.sub.set_text(humanBytes(s.bytes_in) + " total");
            }
        }
        if (this._refs.up) {
            this._refs.up.val.set_text(humanRate(s.rate_out));
            if (this._refs.up.sub) {
                this._refs.up.sub.set_text(humanBytes(s.bytes_out) + " total");
            }
        }
        if (this._refs.uptimeTile) {
            this._refs.uptimeTile.val.set_text(humanDur(s.uptime_sec || 0));
        }
    },

    _patchConnectedCompact: function (s) {
        if (this._refs.rateIn) {
            this._refs.rateIn.set_text("↓ " + humanRate(s.rate_in));
        }
        if (this._refs.rateOut) {
            this._refs.rateOut.set_text("↑ " + humanRate(s.rate_out));
        }
    },

    _render: function () {
        let s = this._status;
        let kind = this._kindOf(s);

        // Same view as last tick → patch in place (no destroy_all). The
        // connected views are the hot path: 2 Hz updates with rates,
        // uptime, and the sparkline. Other kinds are static between
        // transitions, so patching is a no-op for them.
        if (kind === this._currentKind) {
            this._patch(kind, s);
            return;
        }

        // Kind changed → rebuild. Cancel any running animations from
        // the outgoing view, drop refs, then full repaint.
        this._stopAnims();
        this._refs = {};
        this._root.destroy_all_children();
        this._currentKind = kind;
        this._renderHead();

        if (this._picking) {
            this._renderPicker();
            return;
        }
        if (!s) {
            this._renderStatusRow("○", "gray", "Loading…", "");
            return;
        }
        switch (s.state) {
            case "connected":
                this._renderConnected(s);
                break;
            case "connecting":
                this._renderConnecting(s);
                break;
            case "failed":
                this._renderFailed(s);
                break;
            case "error":
                this._renderError(s);
                break;
            default:
                this._renderDisconnected(s);
        }
    },

    _renderHead: function () {
        let head = new St.BoxLayout({ style_class: "o3ui-head" });
        let title = new St.Label({
            text: "ovpn3",
            style_class: "o3ui-head-title",
        });
        head.add(title, { expand: true });
        let menu = new St.Button({
            label: "⋯",
            style_class: "o3ui-head-menu",
        });
        // Head ⋯ acts as the universal "manage" affordance: from any
        // state it opens the picker, which in turn has an "open app"
        // escape hatch to the full TUI.
        menu.connect("clicked", Lang.bind(this, function () {
            if (this._picking) this._closePicker();
            else this._openPicker();
        }));
        head.add(menu);
        this._root.add(head);
    },

    _renderStatusRow: function (glyph, dotColor, state, sub, stateExtraClass) {
        let row = new St.BoxLayout({ style_class: "o3ui-status-row" });
        let dot = new St.Bin({
            style_class: "o3ui-dot o3ui-dot-" + dotColor,
        });
        row.add(dot);
        let label = new St.BoxLayout({ vertical: true });
        let stateClass = "o3ui-state";
        if (stateExtraClass) stateClass += " " + stateExtraClass;
        let stateLbl = new St.Label({ text: state, style_class: stateClass });
        label.add(stateLbl);
        let subLbl = null;
        if (sub) {
            subLbl = new St.Label({ text: sub, style_class: "o3ui-sub" });
            label.add(subLbl);
        }
        row.add(label, { expand: true });
        this._root.add(row);
        // Stash refs so _patch() can update text without rebuilding.
        // _patch knows the layout it's mutating and reads what it needs.
        this._refs.dot = dot;
        this._refs.state = stateLbl;
        this._refs.sub = subLbl;
    },

    _renderDisconnected: function (s) {
        // Without an explicit user setting we lean on the CLI's
        // default_profile field: it surfaces the favorite first (then
        // most recently used, then the first imported profile). A
        // freshly-added desklet already shows the user's preferred
        // tunnel without them having to touch Configure.
        let target = this._targetProfile(s);
        let sub = target
            ? "ready: " + target + (s.default_reason === "favorite" ? " ★" : "")
            : "no profile imported";
        this._renderStatusRow("○", "gray", "Disconnected", sub);

        let btn = new St.Button({
            label: target ? ("▶  Connect to " + target) : "▶  Connect",
            style_class: "o3ui-btn o3ui-btn-connect",
            reactive: !!target,
        });
        btn.connect("clicked", Lang.bind(this, this._connect));
        this._root.add(btn);

        this._renderFoot(target || "—", "change ›",
            Lang.bind(this, this._openPicker));
    },

    // _targetProfile resolves which profile the Connect button drives:
    // explicit user setting first, then the CLI-supplied default
    // (favorite / last-used / first), so the desklet is useful even
    // before the user opens Configure.
    _targetProfile: function (s) {
        if (this._selected && this._selected.length > 0) return this._selected;
        if (this.profile && this.profile.length > 0) return this.profile;
        if (s && s.profile) return s.profile;
        if (s && s.default_profile) return s.default_profile;
        return "";
    },

    _renderConnecting: function (s) {
        this._renderStatusRow("●", "amber", "Connecting…", s.profile || "");
        // Heartbeat the amber dot — pure opacity loop, light on CPU.
        // The pulsing telegraphs "we're working on it" while the
        // status line below stays the source of truth for what step.
        this._startPulse(this._refs.dot);

        // Indeterminate progress strip: a fixed-width pill slides
        // through the track left-to-right and loops. Matches the
        // design's `@keyframes prog` and serves the same purpose —
        // openvpn3 doesn't report a real percent, so showing motion
        // is more honest than a fake stalled-percentage fill.
        const TRACK_W = 256;
        const FILL_W = 90;
        let track = new St.Widget({
            style_class: "o3ui-prog-track",
            clip_to_allocation: true,
        });
        track.set_width(TRACK_W);
        track.set_height(3);
        let fill = new St.Widget({ style_class: "o3ui-prog-fill" });
        fill.set_width(FILL_W);
        fill.set_height(3);
        track.add_actor(fill);
        fill.set_position(-FILL_W, 0);
        this._root.add(track);
        this._startProgressSlide(fill, -FILL_W, TRACK_W);

        // Honest status line: whatever openvpn3 last reported. This
        // replaces the previous fake dns/tls/auth/pull/tun0 ribbon —
        // we don't actually know which phase the daemon is in, and
        // showing made-up steps with a real failure message below was
        // worse than showing nothing.
        let msg = new St.BoxLayout({
            vertical: true,
            style_class: "o3ui-err",
        });
        msg.add(new St.Label({
            text: s.message || "establishing tunnel…",
            style_class: "o3ui-sub",
        }));
        this._root.add(msg);

        let btn = new St.Button({
            label: "✕  Cancel",
            style_class: "o3ui-btn o3ui-btn-cancel",
        });
        btn.connect("clicked", Lang.bind(this, this._disconnect));
        this._root.add(btn);

        this._renderFoot(s.profile || "—", "", null);
    },

    _renderConnected: function (s) {
        if (this.compact) {
            this._renderConnectedCompact(s);
            return;
        }
        let sub = [];
        if (s.country) sub.push(s.country);
        if (s.uptime_sec) sub.push(humanDur(s.uptime_sec));
        this._renderStatusRow("●", "green", "Connected", sub.join(" · ") || s.profile);

        // 2x2 stats grid. Each tile returns { box, val, sub } so the
        // _patch path can mutate the value/sub labels in place.
        let grid = new St.BoxLayout({
            vertical: true,
            style_class: "o3ui-stats",
        });
        let topRow = new St.BoxLayout();
        let down = this._statTile("↓ Down", humanRate(s.rate_in),
            humanBytes(s.bytes_in) + " total", "o3ui-stat-val-down");
        let up = this._statTile("↑ Up", humanRate(s.rate_out),
            humanBytes(s.bytes_out) + " total", "o3ui-stat-val-up", true);
        topRow.add(down.box);
        topRow.add(up.box);
        grid.add(topRow);
        let botRow = new St.BoxLayout();
        let prof = this._statTile("Profile",
            (s.profile || "—").slice(0, 14), "", "o3ui-stat-val-neutral", false, true);
        let upt = this._statTile("Uptime",
            humanDur(s.uptime_sec || 0), "since " + (s.started_at || "—").slice(11, 16),
            "o3ui-stat-val-neutral", true, true);
        botRow.add(prof.box);
        botRow.add(upt.box);
        grid.add(botRow);
        this._root.add(grid);
        this._refs.down = down;
        this._refs.up = up;
        this._refs.profileTile = prof;
        this._refs.uptimeTile = upt;

        // Sparkline strip via a DrawingArea — Clutter canvas.
        this._root.add(this._sparkline());

        let btn = new St.Button({
            label: "■  Disconnect",
            style_class: "o3ui-btn o3ui-btn-disconnect",
        });
        btn.connect("clicked", Lang.bind(this, this._disconnect));
        this._root.add(btn);

        this._renderFoot(s.profile || "—", "", null);
    },

    // _renderConnectedCompact is the 240px minimal variant from design
    // state #4: compressed head, single status line with profile +
    // uptime, one inline rates strip, mini sparkline (no divider),
    // and a slim full-width Disconnect bar at the bottom.
    _renderConnectedCompact: function (s) {
        let sub = [humanDur(s.uptime_sec || 0)];
        if (s.country) sub.push(s.country);
        this._renderStatusRow("●", "green",
            s.profile || "Connected", sub.join(" · "));

        let rates = new St.BoxLayout({ style_class: "o3ui-stats-inline" });
        let rateInLbl = new St.Label({
            text: "↓ " + humanRate(s.rate_in),
            style_class: "o3ui-stats-inline-down",
        });
        let rateOutLbl = new St.Label({
            text: "↑ " + humanRate(s.rate_out),
            style_class: "o3ui-stats-inline-up",
        });
        rates.add(rateInLbl);
        rates.add(rateOutLbl);
        this._root.add(rates);
        this._refs.rateIn = rateInLbl;
        this._refs.rateOut = rateOutLbl;

        // Mini-sparkline reuses the same _sparkline machinery but the
        // wrapping Box gets a tighter padding via the -mini override.
        let spark = this._sparkline();
        spark.set_style_class_name("o3ui-spark o3ui-spark-mini");
        this._root.add(spark);

        let btn = new St.Button({
            label: "■  Disconnect",
            style_class: "o3ui-btn-slim",
        });
        btn.connect("clicked", Lang.bind(this, this._disconnect));
        this._root.add(btn);
    },

    _renderFailed: function (s) {
        this._renderStatusRow("●", "red", "Connection failed",
            s.message || "unknown error", "o3ui-state-failed");
        let err = new St.BoxLayout({ vertical: true, style_class: "o3ui-err" });
        err.add(new St.Label({ text: s.message || "openvpn3 reported a failure." }));
        if (s.why) err.add(new St.Label({ text: s.why, style_class: "o3ui-err-why" }));
        this._root.add(err);

        let btn = new St.Button({
            label: "↻  Retry",
            style_class: "o3ui-btn o3ui-btn-retry",
        });
        btn.connect("clicked", Lang.bind(this, this._connect));
        this._root.add(btn);

        this._renderFoot(s.profile || "—", "view log ›", Lang.bind(this, this._openTUI));
    },

    _renderError: function (s) {
        this._renderStatusRow("●", "red", "o3ui unavailable",
            "is the CLI on $PATH?", "o3ui-state-failed");
        let err = new St.BoxLayout({ vertical: true, style_class: "o3ui-err" });
        err.add(new St.Label({ text: s.message || "" }));
        err.add(new St.Label({
            text: "set cli_path in desklet settings to point at the o3ui binary",
            style_class: "o3ui-err-why",
        }));
        this._root.add(err);
    },

    // _statTile returns { box, valLabel, subLabel } so _patch can flip
    // numbers without rebuilding the surrounding chrome.
    _statTile: function (label, value, sub, valClass, last, bottom) {
        let cls = "o3ui-stat";
        if (last) cls += " o3ui-stat-last";
        if (bottom) cls += " o3ui-stat-bottom";
        let box = new St.BoxLayout({ vertical: true, style_class: cls });
        box.add(new St.Label({ text: label, style_class: "o3ui-stat-label" }));
        let valLbl = new St.Label({
            text: value || "—",
            style_class: "o3ui-stat-val " + (valClass || ""),
        });
        box.add(valLbl);
        let subLbl = null;
        if (sub) {
            subLbl = new St.Label({ text: sub, style_class: "o3ui-stat-sub" });
            box.add(subLbl);
        }
        return { box: box, val: valLbl, sub: subLbl };
    },

    // _sparkline renders the throughput ring buffer as two overlaid
    // line plots — green for downstream, peach for upstream — using
    // a Clutter canvas. The series come from the CLI cache so they
    // survive across polls.
    _sparkline: function () {
        let wrap = new St.BoxLayout({ vertical: true, style_class: "o3ui-spark" });
        let legend = new St.BoxLayout();
        let left = new St.Label({
            text: "throughput · 60s",
            style_class: "o3ui-spark-legend",
        });
        legend.add(left, { expand: true });
        let peak = 0;
        for (let i = 0; i < this._sparkBytesIn.length; i++) {
            if (this._sparkBytesIn[i] > peak) peak = this._sparkBytesIn[i];
        }
        let right = new St.Label({
            text: "peak " + humanRate(peak),
            style_class: "o3ui-spark-legend",
        });
        legend.add(right);
        wrap.add(legend);
        // Cached so _patch can update the peak readout and trigger a
        // canvas repaint without rebuilding the sparkline tree.
        this._refs.sparkPeak = right;

        // DrawingArea has no flex width and St's allocation pulls the
        // parent BoxLayout to fit its preferred size — so a too-wide
        // area drags the whole card past the painted background. Pick
        // values comfortably below the card content area:
        //   default: 288 - 2(border) - 24(padding) = 262 → use 256
        //   compact: 240 - 2(border) - 20(padding) = 218 → use 200
        let sparkW = this.compact ? 200 : 256;
        let sparkH = this.compact ? 22 : 32;
        let area = new St.DrawingArea();
        area.set_width(sparkW);
        area.set_height(sparkH);
        // Read series fresh on every repaint so _patch can just call
        // queue_repaint() — no need to swap the closure each tick.
        area.connect("repaint", Lang.bind(this, function () {
            this._paintSpark(area, this._sparkBytesIn, this._sparkBytesOut);
        }));
        wrap.add(area);
        this._refs.sparkArea = area;
        return wrap;
    },

    // _paintSpark draws the two throughput series. We pad each input
    // up to a fixed window length with leading zeros so the line
    // starts on the baseline instead of stretching a degenerate two-
    // point chart diagonally across the whole strip — that artefact
    // dominated the first ~minute of every connection.
    _paintSpark: function (area, downs, ups) {
        const WINDOW = 60;
        let cr = area.get_context();
        let [w, h] = area.get_surface_size();
        cr.setLineWidth(1.5);
        cr.setLineJoin(1); // round
        let series = [
            { vals: padLead(downs, WINDOW), r: 0.65, g: 0.89, b: 0.63, a: 0.9 },
            { vals: padLead(ups, WINDOW),   r: 0.98, g: 0.70, b: 0.53, a: 0.85 },
        ];
        // Shared y-scale across both lines, so up and down keep their
        // relative magnitudes — picking a per-series max made a 200 B/s
        // upload look as tall as a 5 MB/s download.
        let max = 0;
        for (let i = 0; i < series.length; i++) {
            for (let j = 0; j < series[i].vals.length; j++) {
                if (series[i].vals[j] > max) max = series[i].vals[j];
            }
        }
        if (max <= 0) {
            // No throughput yet — draw a flat baseline so the strip
            // doesn't look broken on a freshly connected session.
            cr.setSourceRGBA(0.65, 0.89, 0.63, 0.4);
            cr.moveTo(0, h - 2);
            cr.lineTo(w, h - 2);
            cr.stroke();
            return;
        }
        for (let i = 0; i < series.length; i++) {
            let v = series[i].vals;
            cr.setSourceRGBA(series[i].r, series[i].g, series[i].b, series[i].a);
            cr.moveTo(0, h - (v[0] / max) * (h - 4) - 2);
            for (let j = 1; j < v.length; j++) {
                let x = (j / (v.length - 1)) * w;
                let y = h - (v[j] / max) * (h - 4) - 2;
                cr.lineTo(x, y);
            }
            cr.stroke();
        }
    },

    _renderFoot: function (text, switchText, switchAction) {
        let foot = new St.BoxLayout({ style_class: "o3ui-foot" });
        foot.add(new St.Label({ text: text }), { expand: true });
        if (switchText) {
            let sw = new St.Button({
                label: switchText,
                style_class: "o3ui-foot-switch",
            });
            if (switchAction) sw.connect("clicked", switchAction);
            foot.add(sw);
        }
        this._root.add(foot);
    },

    _connect: function () {
        let target = this._targetProfile(this._status);
        if (!target) return; // nothing to connect to
        this._rpc.call("connect", { target: target },
            Lang.bind(this, function () { this._poll(); }));
    },

    _disconnect: function () {
        this._rpc.call("disconnect", {},
            Lang.bind(this, function () { this._poll(); }));
    },

    // ── picker ───────────────────────────────────────────────────

    _openPicker: function () {
        // Refetch profiles every time the picker opens so the list
        // reflects imports/removes that happened since startup. Cheap
        // — list is a single overlay read.
        this._rpc.call("list", {}, Lang.bind(this, function (result, err) {
            if (err) {
                this._status = { state: "error", message: err };
                this._render();
                return;
            }
            this._profiles = result || [];
            this._picking = true;
            this._render();
        }));
    },

    _closePicker: function () {
        this._picking = false;
        this._render();
    },

    _pickProfile: function (name) {
        // Picking *selects* the profile — it does not fire a connect.
        // The user lands back on the main view with the Connect button
        // pointed at the chosen profile; they explicitly press it to
        // start the tunnel. This separation matters because connect
        // can take seconds (or fail on auth) and we want the desklet
        // to reflect the user's choice immediately, regardless.
        this._selected = name;
        this._picking = false;
        this._render();
    },

    _renderPicker: function () {
        let n = this._profiles.length;
        this._renderStatusRow("○", "gray", "Choose profile",
            n + " imported · click to connect");

        let pickerBox = new St.BoxLayout({
            vertical: true,
            style_class: "o3ui-picker",
        });
        pickerBox.add(new St.Label({
            text: "Profiles",
            style_class: "o3ui-picker-title",
        }));
        let curLow = this._targetProfile(this._status).toLowerCase();
        for (let i = 0; i < this._profiles.length; i++) {
            pickerBox.add(this._renderPickerItem(this._profiles[i], curLow));
        }
        if (n === 0) {
            pickerBox.add(new St.Label({
                text: "(no profiles imported — run o3ui and press i)",
                style_class: "o3ui-sub",
            }));
        }
        this._root.add(pickerBox);

        let foot = new St.BoxLayout({ style_class: "o3ui-foot" });
        let cancel = new St.Button({
            label: "✕ cancel",
            style_class: "o3ui-foot-switch",
        });
        cancel.connect("clicked", Lang.bind(this, this._closePicker));
        foot.add(cancel, { expand: true });
        let openApp = new St.Button({
            label: "⚙ open app",
            style_class: "o3ui-foot-switch",
        });
        openApp.connect("clicked", Lang.bind(this, this._openTUI));
        foot.add(openApp);
        this._root.add(foot);
    },

    _renderPickerItem: function (profile, curLow) {
        let on = profile.name.toLowerCase() === curLow;
        let row = new St.BoxLayout({
            style_class: on ? "o3ui-picker-item o3ui-picker-item-on"
                            : "o3ui-picker-item",
            reactive: true,
            track_hover: true,
        });
        let cc = (profile.country || profile.name.slice(0, 2)).toUpperCase().slice(0, 2);
        row.add(new St.Label({
            text: cc,
            style_class: "o3ui-picker-flag",
        }));
        row.add(new St.Label({
            text: profile.name,
            style_class: "o3ui-picker-name",
        }), { expand: true });
        if (profile.favorite) {
            row.add(new St.Label({
                text: "★",
                style_class: "o3ui-picker-ping",
            }));
        }
        if (on) {
            row.add(new St.Label({
                text: "●",
                style_class: "o3ui-foot-switch",
            }));
        }
        // Make the whole row clickable. St.Button would force its own
        // chrome; an explicit ClickAction on the BoxLayout keeps the
        // flat row look from the design.
        let click = new Clutter.ClickAction();
        click.connect("clicked", Lang.bind(this, function () {
            this._pickProfile(profile.name);
        }));
        row.add_action(click);
        return row;
    },

    _openTUI: function () {
        // Launch the TUI in the user's default terminal. Cinnamon picks
        // up x-terminal-emulator on most distros; fall back to gnome-
        // terminal which Mint ships by default.
        Util.spawn(["x-terminal-emulator", "-e", this._cliBin()]);
    },
};

// padLead returns the input padded on the left with zeros so its
// length matches `target`. Used by the sparkline painter so the line
// always covers the full strip — early in a session we may only have
// 2-3 real samples and the chart would otherwise stretch them across
// the whole width as a misleading diagonal.
function padLead(arr, target) {
    arr = arr || [];
    if (arr.length >= target) return arr.slice(arr.length - target);
    let out = new Array(target - arr.length).fill(0);
    return out.concat(arr);
}

function humanRate(bps) {
    if (!bps || bps < 0) return "0 B/s";
    if (bps < 1024) return bps + " B/s";
    if (bps < 1024 * 1024) return (bps / 1024).toFixed(1) + " KB/s";
    return (bps / (1024 * 1024)).toFixed(1) + " MB/s";
}

function main(metadata, desklet_id) {
    return new MyDesklet(metadata, desklet_id);
}
