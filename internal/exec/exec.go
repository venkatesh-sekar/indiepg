// Package exec abstracts shell command execution behind a Runner interface so
// every external side effect (systemctl, apt, pgbackrest, pg_dump, ...) is
// unit-testable with a Fake. It supports running as another OS user (sudo -u)
// and a dry-run mode that records commands without executing them.
package exec

import (
	"context"
	"time"
)

// RunSpec describes a single command invocation.
type RunSpec struct {
	// Name is the executable (looked up on PATH), e.g. "systemctl".
	Name string
	// Args are the arguments, not including Name.
	Args []string
	// AsUser, when non-empty, runs the command as that OS user via "sudo -u".
	AsUser string
	// Stdin is fed to the process's standard input, if non-empty.
	Stdin string
	// Env are extra environment variables ("KEY=VALUE") layered over the
	// parent environment.
	Env []string
	// Dir is the working directory; empty means inherit the parent's.
	Dir string
	// Timeout bounds the run; zero means no explicit timeout.
	Timeout time.Duration
	// Sensitive marks the command as containing secrets so the Runner must not
	// log the resolved argv (e.g. a SQL statement with a password).
	Sensitive bool
}

// RunResult is the outcome of a command.
type RunResult struct {
	// Command is the fully resolved argv (including any sudo prefix). It is
	// empty when Sensitive was set.
	Command []string
	Stdout  string
	Stderr  string
	// ExitCode is the process exit status (0 on success). It is set even when
	// Run returns a non-nil error for a non-zero exit.
	ExitCode int
	// Duration is how long the command took.
	Duration time.Duration
	// DryRun is true when the command was recorded but not executed.
	DryRun bool
}

// Success reports whether the command exited zero.
func (r RunResult) Success() bool { return r.ExitCode == 0 }

// Runner executes commands. Implementations must honor ctx cancellation and the
// RunSpec.Timeout, and must return a *core.Error (CodeExec) wrapping the cause
// on failure. A non-zero exit code is reported both via the returned error and
// RunResult.ExitCode.
type Runner interface {
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
	// DryRun reports whether this Runner records commands instead of executing
	// them.
	DryRun() bool
}
