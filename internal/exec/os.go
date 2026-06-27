package exec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// OSRunner is the real Runner backed by os/exec. When dryRun is true it records
// and logs commands without executing them, returning a successful RunResult.
type OSRunner struct {
	log    *core.Logger
	dryRun bool
}

// NewOSRunner builds an OSRunner. A nil logger is replaced with a discard
// logger.
func NewOSRunner(log *core.Logger, dryRun bool) *OSRunner {
	if log == nil {
		log = core.Discard()
	}
	return &OSRunner{log: log, dryRun: dryRun}
}

// DryRun reports whether the runner is in dry-run mode.
func (r *OSRunner) DryRun() bool { return r.dryRun }

// Run executes spec, honoring AsUser (sudo -u), Stdin, Env, Dir, and Timeout.
func (r *OSRunner) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	argv := resolveArgv(spec)

	display := "<sensitive>"
	if !spec.Sensitive {
		display = strings.Join(argv, " ")
	}

	if r.dryRun {
		r.log.Info("dry-run command", "cmd", display)
		return RunResult{Command: nonSensitiveCommand(spec, argv), DryRun: true}, nil
	}

	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	r.log.Debug("running command", "cmd", display)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	// cmd.ProcessState is nil when the binary cannot be started (not found,
	// permission denied). Guard against a nil-pointer panic that would kill
	// the entire panel process — especially dangerous in the async StartBackup
	// goroutine which has no recover().
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	res := RunResult{
		Command:  nonSensitiveCommand(spec, argv),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: dur,
	}

	if err != nil {
		// Fold the command's own stderr into the message, not just a structured
		// detail: callers render the error string (e.g. backup_history.error), and
		// without this the operator sees only "exit status N" with no clue WHY
		// (pgBackRest, pg_dump, etc. print the actionable reason to stderr).
		tail := stderrTail(spec, res.Stderr)
		if ctx.Err() == context.DeadlineExceeded {
			return res, core.ExecError("command timed out after %s: %s%s", spec.Timeout, display, tail).
				WithDetail("stderr", res.Stderr).Wrap(err)
		}
		return res, core.ExecError("command failed (exit %d): %s%s", res.ExitCode, display, tail).
			WithDetail("stderr", res.Stderr).Wrap(err)
	}
	return res, nil
}

// stderrTail returns a compact, message-appendable form of a failed command's
// stderr, or "" when there is nothing useful to add. It is omitted for Sensitive
// commands (whose output may carry secrets) and capped to a sane length, keeping
// the TAIL because tools print the decisive error line last.
func stderrTail(spec RunSpec, stderr string) string {
	if spec.Sensitive {
		return ""
	}
	s := strings.TrimSpace(stderr)
	if s == "" {
		return ""
	}
	const max = 800
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return ": " + s
}

// resolveArgv builds the full argv, prepending "sudo -u <user>" when AsUser is
// set.
func resolveArgv(spec RunSpec) []string {
	argv := make([]string, 0, len(spec.Args)+3)
	if spec.AsUser != "" {
		argv = append(argv, "sudo", "-u", spec.AsUser)
	}
	argv = append(argv, spec.Name)
	argv = append(argv, spec.Args...)
	return argv
}

func nonSensitiveCommand(spec RunSpec, argv []string) []string {
	if spec.Sensitive {
		return nil
	}
	return argv
}
