package exec

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
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

	res := RunResult{
		Command:  nonSensitiveCommand(spec, argv),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
		Duration: dur,
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return res, core.ExecError("command timed out after %s: %s", spec.Timeout, display).
				WithDetail("stderr", res.Stderr).Wrap(err)
		}
		return res, core.ExecError("command failed (exit %d): %s", res.ExitCode, display).
			WithDetail("stderr", res.Stderr).Wrap(err)
	}
	return res, nil
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
