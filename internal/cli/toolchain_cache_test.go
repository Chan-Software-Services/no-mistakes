package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain points HOME at a throwaway directory. Left alone, every `go build`
// a test runs would then resolve GOMODCACHE and GOCACHE under that directory:
// it re-downloads every module and rebuilds the standard library, and the
// module cache it extracts is read-only, so the os.RemoveAll at the end of
// TestMain fails and the whole cache (hundreds of MB) stays in /tmp for good.
// Enough of those fill the tmpfs quota and fail unrelated packages with
// "disk quota exceeded".
func TestGoBuildsKeepTheirCachesOutsideTheTestHome(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("TestMain did not set HOME")
	}
	out, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	values := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(values) != 2 {
		t.Fatalf("go env GOMODCACHE GOCACHE printed %d lines, want 2: %q", len(values), out)
	}
	for i, name := range []string{"GOMODCACHE", "GOCACHE"} {
		value := strings.TrimSpace(values[i])
		if value == "" {
			t.Errorf("%s is empty", name)
			continue
		}
		rel, err := filepath.Rel(home, value)
		if err != nil {
			continue
		}
		if rel == "." || !strings.HasPrefix(rel, "..") {
			t.Errorf("%s = %s is inside the throwaway test HOME %s", name, value, home)
		}
	}
}
