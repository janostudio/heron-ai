//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// binPath is the engine binary built once for the whole package. Building per
// test would hide compile errors behind a slow test and make every case pay the
// same cost; building once in TestMain keeps the failure mode "the package
// never started" instead of "12 tests failed for the same reason".
var binPath string

func TestMain(m *testing.M) {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "locate repo root: %v\n", err)
		os.Exit(1)
	}

	// One run directory for the whole process: cases append verdicts to it as
	// they finish, and the report is written even if the process dies mid-run.
	runDir := os.Getenv("HERON_E2E_RUN_DIR")
	if runDir == "" {
		base := os.Getenv("HERON_E2E_ARTIFACTS")
		if base == "" {
			// Default outside the checkout: the run must not dirty the worktree.
			base = filepath.Join(os.TempDir(), "heron-e2e-reports")
		}
		runDir = filepath.Join(base, time.Now().Format("2006-01-02-150405"))
	}
	if mkErr := os.MkdirAll(runDir, 0o755); mkErr != nil {
		fmt.Fprintf(os.Stderr, "create run dir: %v\n", mkErr)
		os.Exit(1)
	}
	_ = os.Setenv("HERON_E2E_RUN_DIR", runDir)
	fmt.Fprintf(os.Stderr, "e2e run dir: %s\n", runDir)

	tmp, err := os.MkdirTemp("", "heron-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmp, "heron")

	// Stamp the build the way a release does, so the health case can assert the
	// value travels from build flag to HTTP response instead of trusting that
	// the wiring compiles.
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X main.version=e2e-build -X main.commit=e2e-commit",
		"-o", binPath, "./cmd/server")
	build.Dir = root
	build.Env = os.Environ()
	out, buildErr := build.CombinedOutput()
	if buildErr != nil {
		// Fail fast and loudly: a broken build reported as "all tests skipped"
		// is worse than no tests at all.
		fmt.Fprintf(os.Stderr, "engine build failed: %v\n%s", buildErr, out)
		os.Exit(1)
	}

	code := m.Run()
	finalizeReport()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// repoRoot is the module root, derived from this file's package directory.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(wd, "..", ".."))
}
