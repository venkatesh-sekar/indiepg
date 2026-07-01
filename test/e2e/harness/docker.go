//go:build e2e

package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// dockerEnv returns the process environment with DOCKER_CONTEXT forced to the
// live daemon (the active desktop-linux context points at a dead socket in this
// environment — see the design's proven host facts).
func dockerEnv() []string {
	return append(os.Environ(), "DOCKER_CONTEXT=default")
}

// runCmd runs an arbitrary command with the docker env, returning stdout, stderr
// and the error. A non-zero exit is returned as the error with stderr folded in.
func runCmd(ctx context.Context, name string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = dockerEnv()
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	stdout, stderr = so.String(), se.String()
	if err != nil {
		err = fmt.Errorf("%s %s: %w\nstderr: %s", name, strings.Join(args, " "), err, strings.TrimSpace(se.String()))
	}
	return stdout, stderr, err
}

// compose runs `docker compose -p <project> -f <file> <args...>` with the given
// extra environment (PANEL_IMAGE, MINIO_*). It returns combined-ish output.
func (e *Env) compose(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	full := append([]string{"compose", "-p", e.Project, "-f", composeFile()}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Env = append(dockerEnv(), extraEnv...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	out := so.String() + se.String()
	if err != nil {
		return out, fmt.Errorf("docker compose %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

// dockerExec runs `docker exec [-u user] <container> <argv...>` and returns
// stdout, stderr, error. It is the single seam every in-container action goes
// through (PG psql, systemctl, indiepg install, file ops).
func dockerExec(ctx context.Context, container, user string, argv ...string) (stdout, stderr string, err error) {
	args := []string{"exec"}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, container)
	args = append(args, argv...)
	return runCmd(ctx, "docker", args...)
}

// shortCtx bounds a quick docker call so a wedged daemon cannot hang a test.
func shortCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}
