// Package agent implements an agentic question-answering loop that plans,
// invokes read-only higgs subcommands, and synthesizes a grounded
// answer. Tool discovery is performed by subprocessing `higgs schema`.
// Tool invocation is also subprocess-based against the same binary, which
// keeps the agent's blast radius bounded to the current process's binary
// and shell environment.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// DefaultAllowedTools is the curated, read-only whitelist of higgs
// subcommand names the agent may invoke. Callers may override via
// Options.AllowedTools, but the tool loop enforces allow-list membership
// strictly; any planned step referencing a tool outside this set is
// rejected before any subprocess is spawned.
var DefaultAllowedTools = []string{
	"search",
	"fetch-and-parse",
	"scan-folders",
	"summarize",
	"digest",
	"thread",
	"threads",
	"attachments",
	"state",
}

// Tool captures the subset of schema metadata the agent needs to reason
// about and invoke a subcommand.
type Tool struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	StdoutFormat string   `json:"stdout_format,omitempty"`
	ExitCodes    []int    `json:"exit_codes,omitempty"`
	Flags        []string `json:"flags,omitempty"`
	Args         []string `json:"args,omitempty"`
}

// schemaFlag mirrors cmd_schema.schemaFlag (minimal shape).
type schemaFlag struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// schemaArg mirrors cmd_schema.schemaArg.
type schemaArg struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
}

// schemaCommand mirrors cmd_schema.schemaCommand (minimal shape).
type schemaCommand struct {
	Name      string          `json:"name"`
	Short     string          `json:"short"`
	Long      string          `json:"long,omitempty"`
	Flags     []schemaFlag    `json:"flags"`
	Args      []schemaArg     `json:"args"`
	Stdout    string          `json:"stdout,omitempty"`
	ExitCodes []int           `json:"exit_codes"`
	Commands  []schemaCommand `json:"commands,omitempty"`
}

type schemaRoot struct {
	Commands []schemaCommand `json:"commands"`
}

// DiscoverTools runs `<binPath> schema` and filters the resulting commands
// to the allow list. An empty allow list returns an empty slice — the
// agent never treats "no allow list" as "allow everything" because that
// would let the model invoke write-capable commands.
func DiscoverTools(binPath string, allow []string) ([]Tool, error) {
	if binPath == "" {
		return nil, errors.New("bin path required")
	}
	if len(allow) == 0 {
		return []Tool{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "schema")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run schema: %w (stderr: %s)", err, stderr.String())
	}
	var root schemaRoot
	if err := json.Unmarshal(stdout.Bytes(), &root); err != nil {
		return nil, fmt.Errorf("parse schema json: %w", err)
	}
	allowSet := make(map[string]struct{}, len(allow))
	for _, a := range allow {
		allowSet[a] = struct{}{}
	}
	out := make([]Tool, 0, len(allow))
	for _, c := range root.Commands {
		if _, ok := allowSet[c.Name]; !ok {
			continue
		}
		t := Tool{
			Name:         c.Name,
			Description:  c.Short,
			StdoutFormat: c.Stdout,
			ExitCodes:    c.ExitCodes,
		}
		for _, f := range c.Flags {
			t.Flags = append(t.Flags, f.Name)
		}
		for _, a := range c.Args {
			t.Args = append(t.Args, a.Name)
		}
		out = append(out, t)
	}
	return out, nil
}

// Invocation describes a single planned tool call.
type Invocation struct {
	Tool    string        `json:"tool"`
	Args    []string      `json:"args"`
	Timeout time.Duration `json:"-"`
}

// Observation is the recorded outcome of an Invocation.
type Observation struct {
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Duration time.Duration `json:"duration"`
}

// DefaultToolTimeout bounds every subprocess tool invocation.
const DefaultToolTimeout = 30 * time.Second

// Invoke runs binPath with inv.Tool as the first argument and inv.Args as
// the remaining argv, returning stdout, stderr, duration and the process
// exit code. The context deadline (or DefaultToolTimeout) bounds the
// subprocess. A non-zero exit code is reported via Observation.ExitCode
// rather than a Go error so the agent loop can record and continue.
func Invoke(ctx context.Context, binPath string, inv Invocation) (Observation, error) {
	if binPath == "" {
		return Observation{}, errors.New("bin path required")
	}
	if inv.Tool == "" {
		return Observation{}, errors.New("tool name required")
	}
	timeout := inv.Timeout
	if timeout <= 0 {
		timeout = DefaultToolTimeout
	}
	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := append([]string{inv.Tool}, inv.Args...)
	cmd := exec.CommandContext(subCtx, binPath, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	obs := Observation{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: dur,
	}
	if err != nil {
		// Timeout/cancellation always wins over ExitError because a
		// killed child surfaces as a signal exit which we do NOT want to
		// treat as a clean nonzero exit.
		if subCtx.Err() != nil {
			return obs, fmt.Errorf("tool %q timed out after %s: %w", inv.Tool, timeout, subCtx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			obs.ExitCode = exitErr.ExitCode()
			return obs, nil
		}
		return obs, fmt.Errorf("run tool %q: %w", inv.Tool, err)
	}
	obs.ExitCode = cmd.ProcessState.ExitCode()
	return obs, nil
}

// ToolAllowed reports whether name is in the allow list.
func ToolAllowed(name string, allow []string) bool {
	for _, a := range allow {
		if a == name {
			return true
		}
	}
	return false
}
