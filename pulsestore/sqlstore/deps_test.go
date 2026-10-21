package sqlstore_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoDriverInTheBuild guards the dependency rule from
// docs/issues/0001-module-skeleton.md: the library imports no database driver,
// so a project that uses only the agent side never compiles one in, and a
// project that uses sqlstore chooses its own.
//
// The drivers below are imported by this package's TESTS, which is why the
// check inspects the non-test dependency graph specifically.
func TestNoDriverInTheBuild(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	banned := []string{
		"modernc.org/sqlite",
		"github.com/jackc/pgx",
		"github.com/lib/pq",
		"github.com/mattn/go-sqlite3",
	}
	for _, line := range strings.Split(string(out), "\n") {
		pkg := strings.TrimSpace(line)
		for _, b := range banned {
			if strings.HasPrefix(pkg, b) {
				t.Errorf("the build pulls in %s; the application must supply its own driver", pkg)
			}
		}
	}
}
