package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestStateCmdHelp(t *testing.T) {
	// Both subcommands should accept --help without error.
	root := newRootCmd()
	root.SetArgs([]string{"state", "stats", "--help"})
	if _, err := captureStdout(t, func() error { return root.Execute() }); err != nil {
		t.Errorf("state stats --help: %v", err)
	}

	root = newRootCmd()
	root.SetArgs([]string{"state", "clear", "--help"})
	if _, err := captureStdout(t, func() error { return root.Execute() }); err != nil {
		t.Errorf("state clear --help: %v", err)
	}
}

func TestStateStatsEmptyDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("PM_STATE_DB", dbPath)

	stdout, err := captureStdout(t, func() error { return cmdStateStats("") })
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if obj["db"] != dbPath {
		t.Errorf("db = %v, want %q", obj["db"], dbPath)
	}
	mboxes, ok := obj["mailboxes"].([]any)
	if !ok {
		t.Fatalf("mailboxes not array: %T", obj["mailboxes"])
	}
	if len(mboxes) != 0 {
		t.Errorf("expected 0 mailboxes, got %d", len(mboxes))
	}
	total := obj["total"].(map[string]any)
	if total["total"].(float64) != 0 {
		t.Errorf("expected total=0, got %v", total["total"])
	}
}

func TestStateStatsForSpecificMailbox(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("PM_STATE_DB", dbPath)

	stdout, err := captureStdout(t, func() error { return cmdStateStats("Folders/Accounts") })
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if obj["mailbox"] != "Folders/Accounts" {
		t.Errorf("mailbox = %v", obj["mailbox"])
	}
	if obj["total"].(float64) != 0 {
		t.Errorf("total = %v, want 0", obj["total"])
	}
}

func TestStateClearEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("PM_STATE_DB", dbPath)

	stdout, err := captureStdout(t, func() error { return cmdStateClear("SomeMailbox") })
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if obj["cleared"] != true || obj["mailbox"] != "SomeMailbox" {
		t.Errorf("unexpected: %v", obj)
	}
}
