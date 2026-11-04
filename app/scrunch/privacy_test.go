package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestBinaryCannotPhoneHome reads the scrunch binary's complete import graph
// and fails if anything that could talk to the outside world, other than the
// PostgreSQL driver's own sockets, has been linked in.
func TestBinaryCannotPhoneHome(t *testing.T) {
	out, err := exec.Command("go", "list", "-mod=vendor", "-deps", "-f", "{{.ImportPath}} {{join .Imports \",\"}}", ".").Output()
	if err != nil {
		t.Fatalf("listing scrunch dependencies: %v", err)
	}
	// No package in the binary may provide an HTTP, mail, or RPC client.
	forbidden := map[string]bool{"net/http": true, "net/smtp": true, "net/rpc": true, "net/rpc/jsonrpc": true, "plugin": true}
	// scrunchie's own packages may not open sockets or run programs. The
	// PostgreSQL session imports net only to recognize *net.DNSError.
	ownForbidden := map[string]bool{"net": true, "net/http": true, "os/exec": true}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		path, importList, _ := strings.Cut(line, " ")
		if forbidden[path] {
			t.Errorf("scrunch links %s", path)
		}
		own := strings.HasPrefix(path, "github.com/dustinevan/scrunchie/")
		for _, imported := range strings.Split(importList, ",") {
			if own && ownForbidden[imported] && !(path == "github.com/dustinevan/scrunchie/lib/data/postgres" && imported == "net") {
				t.Errorf("%s imports %s", path, imported)
			}
			// Bubble Tea links os/exec for its optional ExecProcess API, which
			// scrunchie never calls. Any other importer needs review.
			if !own && imported == "os/exec" && path != "charm.land/bubbletea/v2" {
				t.Errorf("dependency %s imports os/exec", path)
			}
		}
	}
	if len(lines) < 50 {
		t.Fatalf("dependency list is implausibly short (%d packages); the check did not run", len(lines))
	}
}
