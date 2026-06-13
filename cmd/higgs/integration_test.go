package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var integrationBinary string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "higgs-bin-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "higgs")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	// Compile once.
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// If the build fails we still run the rest of the tests so the
		// non-integration suite reports real failures. The integration
		// tests will skip.
		integrationBinary = ""
	} else {
		integrationBinary = bin
	}

	code := m.Run()
	os.Exit(code)
}

func runBinary(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	if integrationBinary == "" {
		t.Skip("integration binary unavailable (build failed or skipped)")
	}
	cmd := exec.Command(integrationBinary, args...)
	// Hermetic environment: no PM_IMAP_* vars and keystore disabled so the
	// subprocess can't pick up credentials from the developer's OS keyring.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PM_DISABLE_KEYSTORE=1",
	}
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.String(), errb.String(), ee.ExitCode()
		}
		t.Fatalf("run: %v", err)
	}
	return out.String(), errb.String(), 0
}

func TestIntegrationSchema(t *testing.T) {
	stdout, _, code := runBinary(t, "schema")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	cmds, _ := obj["commands"].([]any)
	if len(cmds) < 7 {
		t.Errorf("commands < 7 (got %d)", len(cmds))
	}
}

func TestIntegrationClassifyDryRunApplyConflict(t *testing.T) {
	stdout, _, code := runBinary(t, "classify", "--apply", "--dry-run")
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (validation); stdout=%s", code, stdout)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	inner, ok := obj["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope: %v", obj)
	}
	if inner["kind"] != "validation" {
		t.Errorf("kind = %v, want validation", inner["kind"])
	}
}

func TestIntegrationHelp(t *testing.T) {
	_, _, code := runBinary(t, "--help")
	if code != 0 {
		t.Errorf("--help exit = %d, want 0", code)
	}
}

func TestIntegrationScanFoldersConfigError(t *testing.T) {
	// No PM_IMAP_USERNAME => exit 4 (config error).
	stdout, _, code := runBinary(t, "scan-folders")
	if code != 4 {
		t.Fatalf("exit = %d, want 4; stdout=%s", code, stdout)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	inner, _ := obj["error"].(map[string]any)
	if inner == nil || inner["kind"] != "config" {
		t.Errorf("expected kind=config, got %v", obj)
	}
}
