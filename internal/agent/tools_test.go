package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFakeBin creates a small shell script at path that emits the given
// stdout and exits with the given code. Windows is skipped.
func writeFakeBin(t *testing.T, dir, name, stdout, stderr string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell-script binaries require POSIX")
	}
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n"
	if stdout != "" {
		script += "cat <<'__EOF__'\n" + stdout + "\n__EOF__\n"
	}
	if stderr != "" {
		script += "cat >&2 <<'__EOF__'\n" + stderr + "\n__EOF__\n"
	}
	script += "exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// fakeSchemaJSON is the minimal subset of schema the agent parses.
const fakeSchemaJSON = `{
  "commands": [
    {"name":"search","short":"search mailboxes","flags":[{"name":"from","type":"string","default":"","description":"from"}],"args":[{"name":"mailbox","required":false,"default":""}],"stdout":"ndjson","exit_codes":[0,5]},
    {"name":"move","short":"move messages","flags":[],"args":[],"stdout":"ndjson","exit_codes":[0,5]},
    {"name":"summarize","short":"summarize","flags":[],"args":[],"stdout":"ndjson","exit_codes":[0]}
  ],
  "exit_code_docs": []
}`

func TestDiscoverTools_FiltersToAllowList(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "higgs", fakeSchemaJSON, "", 0)
	tools, err := DiscoverTools(bin, []string{"search", "summarize"})
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("len=%d want 2 (%v)", len(tools), tools)
	}
	names := map[string]bool{}
	for _, t := range tools {
		names[t.Name] = true
	}
	if !names["search"] || !names["summarize"] {
		t.Errorf("unexpected tools: %v", tools)
	}
	if names["move"] {
		t.Errorf("move leaked through allow list")
	}
	for _, tool := range tools {
		if tool.Name == "search" {
			if len(tool.Flags) == 0 || tool.Flags[0] != "from" {
				t.Errorf("search.Flags=%v", tool.Flags)
			}
			if len(tool.Args) == 0 || tool.Args[0] != "mailbox" {
				t.Errorf("search.Args=%v", tool.Args)
			}
		}
	}
}

func TestDiscoverTools_EmptyAllowList(t *testing.T) {
	tools, err := DiscoverTools("/nonexistent", []string{})
	if err != nil {
		t.Fatalf("empty allow list should not error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("want empty, got %v", tools)
	}
}

func TestDiscoverTools_MissingBinPath(t *testing.T) {
	_, err := DiscoverTools("", []string{"search"})
	if err == nil {
		t.Fatal("expected error for empty bin path")
	}
}

func TestDiscoverTools_BinaryFails(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "bad", "", "oh no", 2)
	_, err := DiscoverTools(bin, []string{"search"})
	if err == nil {
		t.Fatal("expected error from failing binary")
	}
	if !strings.Contains(err.Error(), "run schema") {
		t.Errorf("error wrap missing: %v", err)
	}
}

func TestDiscoverTools_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "bad", "not json", "", 0)
	_, err := DiscoverTools(bin, []string{"search"})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse schema json") {
		t.Errorf("parse wrap missing: %v", err)
	}
}

func TestInvoke_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "ok", `{"type":"match","uid":1}`, "some warn", 0)
	obs, err := Invoke(context.Background(), bin, Invocation{Tool: "search", Args: []string{"INBOX"}})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if obs.ExitCode != 0 {
		t.Errorf("exit=%d", obs.ExitCode)
	}
	if !strings.Contains(obs.Stdout, `"type":"match"`) {
		t.Errorf("stdout=%q", obs.Stdout)
	}
	if !strings.Contains(obs.Stderr, "some warn") {
		t.Errorf("stderr=%q", obs.Stderr)
	}
	if obs.Duration <= 0 {
		t.Errorf("duration=%v", obs.Duration)
	}
}

func TestInvoke_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBin(t, dir, "fail", `{"error":"x"}`, "boom", 3)
	obs, err := Invoke(context.Background(), bin, Invocation{Tool: "search"})
	if err != nil {
		t.Fatalf("Invoke returned err for nonzero exit (should record, not fail): %v", err)
	}
	if obs.ExitCode != 3 {
		t.Errorf("exit=%d want 3", obs.ExitCode)
	}
	if !strings.Contains(obs.Stderr, "boom") {
		t.Errorf("stderr missing boom: %q", obs.Stderr)
	}
}

func TestInvoke_EmptyBin(t *testing.T) {
	_, err := Invoke(context.Background(), "", Invocation{Tool: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestInvoke_EmptyTool(t *testing.T) {
	_, err := Invoke(context.Background(), "/bin/true", Invocation{})
	if err == nil {
		t.Fatal("expected error for empty tool")
	}
}

func TestInvoke_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep script needs POSIX")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "slow")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Invoke(context.Background(), path, Invocation{Tool: "x", Timeout: 50 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want timeout message: %v", err)
	}
}

func TestInvoke_NotFound(t *testing.T) {
	_, err := Invoke(context.Background(), "/definitely/not/a/binary/xxxx", Invocation{Tool: "x"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestToolAllowed(t *testing.T) {
	if !ToolAllowed("search", []string{"search", "summarize"}) {
		t.Error("search should be allowed")
	}
	if ToolAllowed("move", []string{"search"}) {
		t.Error("move should not be allowed")
	}
	if ToolAllowed("search", nil) {
		t.Error("nil allow-list should reject")
	}
}

func TestDefaultAllowedTools_ExcludesWrites(t *testing.T) {
	denylist := []string{"move", "trash", "flag", "mark-read", "archive", "apply-labels", "draft", "unsubscribe", "import", "export"}
	for _, d := range denylist {
		if ToolAllowed(d, DefaultAllowedTools) {
			t.Errorf("write command %q must not be in default allow list", d)
		}
	}
}
