// Package buildinfo holds the release stamps goreleaser injects via
// -ldflags at link time. Local `go build` leaves the defaults — the
// about-tab and any `--version` flag should always be safe to read.
package buildinfo

// Version is the semver tag goreleaser builds from (e.g. "v0.0.4").
// "dev" indicates an unstamped build (plain `go build` / make build).
var Version = "dev"

// Commit is the short git SHA the binary was built from.
var Commit = ""

// Date is the build timestamp in RFC3339, set by goreleaser.
var Date = ""
