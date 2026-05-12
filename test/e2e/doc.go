// Package e2e contains end-to-end tests that talk to a real openvpn3 D-Bus
// service on the host. Build-tagged so `go test ./...` ignores them.
//
// Run with:
//
//	go test -tags=e2e ./test/e2e/...
//
// Tests skip themselves when the openvpn3 services are not reachable, so they
// are safe to invoke in environments where openvpn3 is not installed.
package e2e
