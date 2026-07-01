//go:build e2e

package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Options configure Up. The struct is part of the frozen surface; new knobs may
// be ADDED as fields (default zero value must preserve current behaviour), but
// existing fields and the Up signature do not change.
type Options struct {
	// Image selects the panel image. Zero value (ImagePreinstalled) is the default
	// for every non-install scenario; ImageBase is for install-from-scratch.
	Image PanelImage

	// SkipReadyWait, when true, returns from Up as soon as the containers are up and
	// ports are mapped, WITHOUT waiting for the panel's /readyz. The install-from-
	// scratch scenario sets this (nothing is installed yet); it is ignored for the
	// preinstalled image, which always waits for /readyz.
	SkipReadyWait bool

	// PGMajor, when non-zero, is informational only for the base image (the install
	// scenario passes --pg-version itself). Reserved for future per-scenario images.
	PGMajor int
}

// Env is a running, isolated compose project: a systemd panel container, a MinIO
// target, and the typed handles a scenario asserts through. Create it with Up;
// it tears itself down on t.Cleanup.
type Env struct {
	t       *testing.T
	Project string

	Panel *Panel
	PG    *PG
	S3    *S3

	image          PanelImage
	panelContainer string // resolved container id for docker exec
	panelHostPort  string // host port mapped to the panel's 8443
	minioHostPort  string // host port mapped to minio's 443

	closed bool
}

var projSanitize = regexp.MustCompile(`[^a-z0-9]+`)

// Up starts a uniquely-named compose project and returns its handles. It:
//   - generates a parallel-safe project name from the test name + random suffix,
//   - `docker compose up -d` with the chosen panel image,
//   - resolves the panel container id and the ephemeral host ports,
//   - ensures the MinIO bucket exists,
//   - waits for /readyz (preinstalled, or base unless SkipReadyWait),
//   - registers teardown (+ log dump on failure) on t.Cleanup.
//
// A failure in any step fails the test immediately (after best-effort teardown).
func Up(t *testing.T, opts Options) *Env {
	t.Helper()

	e := &Env{
		t:       t,
		Project: projectName(t),
		image:   opts.Image,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	extraEnv := []string{
		"PANEL_IMAGE=" + opts.Image.ref(),
		"MINIO_ROOT_USER=" + MinIOAccessKey,
		"MINIO_ROOT_PASSWORD=" + MinIOSecretKey,
		"MINIO_BUCKET=" + MinIOBucket,
	}

	// Register cleanup BEFORE `up`, so a partial start is still torn down.
	t.Cleanup(e.Close)

	if out, err := e.compose(ctx, extraEnv, "up", "-d", "--remove-orphans"); err != nil {
		t.Fatalf("compose up failed for project %s: %v\n%s", e.Project, err, out)
	}

	// Resolve the panel container id (compose names are project-scoped).
	cid, err := e.serviceContainerID(ctx, "panel")
	if err != nil {
		e.DumpLogs()
		t.Fatalf("resolve panel container: %v", err)
	}
	e.panelContainer = cid

	// Discover the ephemeral host ports.
	e.panelHostPort, err = e.servicePort(ctx, "panel", panelContainerPort)
	if err != nil {
		e.DumpLogs()
		t.Fatalf("discover panel host port: %v", err)
	}
	e.minioHostPort, err = e.servicePort(ctx, "minio", minioContainerPort)
	if err != nil {
		e.DumpLogs()
		t.Fatalf("discover minio host port: %v", err)
	}

	// Wire the typed handles.
	e.Panel = newPanel("http://127.0.0.1:" + e.panelHostPort)
	e.PG = &PG{env: e}
	e.S3, err = newS3("127.0.0.1:"+e.minioHostPort, MinIOBucket)
	if err != nil {
		e.DumpLogs()
		t.Fatalf("build S3 client: %v", err)
	}

	// Wait for systemd to be up so docker-exec actions (psql/systemctl/install) work.
	e.awaitSystemd(ctx)

	// Belt-and-suspenders: ensure the bucket exists even if minio-init lost a race.
	if err := e.ensureBucketReady(); err != nil {
		e.DumpLogs()
		t.Fatalf("ensure MinIO bucket: %v", err)
	}

	// Readiness: the preinstalled panel autostarts, so wait for /readyz. The base
	// image has nothing installed yet, so SkipReadyWait short-circuits it (the
	// install scenario calls AwaitReady itself after running install).
	if e.image == ImagePreinstalled || !opts.SkipReadyWait {
		e.AwaitReady(90 * time.Second)
	}

	return e
}

// AwaitReady polls the panel's public /readyz until it reports ok or the timeout
// elapses. It is called automatically by Up (except for a base image with
// SkipReadyWait); the install scenario calls it again after running install.
func (e *Env) AwaitReady(timeout time.Duration) {
	e.t.Helper()
	Await(e.t, timeout, time.Second, "panel /readyz", func() (bool, error) {
		return e.Panel.Readyz() == nil, nil
	})
}

// awaitSystemd waits until systemd inside the panel container reports running or
// degraded, so subsequent docker-exec actions have a live system bus.
func (e *Env) awaitSystemd(ctx context.Context) {
	e.t.Helper()
	Await(e.t, 120*time.Second, time.Second, "systemd boot", func() (bool, error) {
		out, _, err := dockerExec(ctx, e.panelContainer, "", "systemctl", "is-system-running")
		state := strings.TrimSpace(out)
		return state == "running" || state == "degraded", err
	})
}

// Exec runs a command in the panel container as root and returns combined stdout
// (stderr folded into the error on failure).
func (e *Env) Exec(argv ...string) (string, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	out, _, err := dockerExec(ctx, e.panelContainer, "", argv...)
	return out, err
}

// ExecAsUser runs a command in the panel container as the given OS user.
func (e *Env) ExecAsUser(user string, argv ...string) (string, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	out, _, err := dockerExec(ctx, e.panelContainer, user, argv...)
	return out, err
}

// ExecCapture runs a command in the panel container as root and returns stdout
// and stderr SEPARATELY (no error folding), for callers that must scrape stdout —
// e.g. parsing the one-time admin password from `indiepg install`.
func (e *Env) ExecCapture(argv ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	return dockerExec(ctx, e.panelContainer, "", argv...)
}

// PanelContainer returns the resolved panel container id for ad-hoc docker exec.
func (e *Env) PanelContainer() string { return e.panelContainer }

// SystemctlIsActive returns the `systemctl is-active <unit>` state ("active",
// "inactive", "failed", …). The systemd "is-active" string is returned verbatim;
// errors from a non-active unit are swallowed (systemctl exits non-zero) so the
// caller asserts on the state string.
func (e *Env) SystemctlIsActive(unit string) string {
	ctx, cancel := shortCtx()
	defer cancel()
	out, _, _ := dockerExec(ctx, e.panelContainer, "", "systemctl", "is-active", unit)
	return strings.TrimSpace(out)
}

// Close tears the project down (containers + named volumes + network). It is
// idempotent and safe to call from t.Cleanup. On a FAILED test it first dumps
// logs for diagnosis.
func (e *Env) Close() {
	if e.closed {
		return
	}
	e.closed = true
	if e.t.Failed() {
		e.DumpLogs()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _ = e.compose(ctx, nil, "down", "-v", "--remove-orphans", "-t", "5")
}

// DumpLogs prints the panel journal + compose logs to the test log for diagnosis.
func (e *Env) DumpLogs() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if e.panelContainer != "" {
		if out, _, err := dockerExec(ctx, e.panelContainer, "", "journalctl", "-u", "indiepg", "-u", "postgresql", "-u", "pgbouncer", "--no-pager", "-n", "160"); err == nil {
			e.t.Logf("=== panel journal (indiepg+postgresql+pgbouncer) [%s] ===\n%s", e.Project, out)
		}
		if out, _, err := dockerExec(ctx, e.panelContainer, "", "systemctl", "status", "pgbouncer", "--no-pager", "-l"); err == nil {
			e.t.Logf("=== systemctl status pgbouncer [%s] ===\n%s", e.Project, out)
		}
		// Postgres cluster state + recovery/restore logs: pinpoints a cluster that
		// failed to come back up (e.g. after a restore/PITR) when psql just reports a
		// missing socket.
		if out, _, err := dockerExec(ctx, e.panelContainer, "", "sh", "-c",
			"echo '--- pg_lsclusters ---'; pg_lsclusters 2>&1; "+
				"echo '--- systemctl --failed ---'; systemctl --no-pager --failed 2>&1; "+
				"echo '--- ls /var/run/postgresql ---'; ls -la /var/run/postgresql /run/postgresql 2>&1; "+
				"echo '--- postgres cluster log (recovery-relevant tail) ---'; grep -hvF 'the database system is starting up' /var/log/postgresql/*.log 2>/dev/null | tail -n 80; "+
				"echo '--- pgbackrest restore log (tail) ---'; tail -n 40 /var/log/pgbackrest/*restore*.log 2>&1"); err == nil {
			e.t.Logf("=== postgres cluster diagnostics [%s] ===\n%s", e.Project, out)
		}
	}
	if out, err := e.compose(ctx, nil, "logs", "--no-color", "--tail", "60"); err == nil {
		e.t.Logf("=== compose logs [%s] ===\n%s", e.Project, out)
	}
}

// serviceContainerID resolves the container id for a compose service.
func (e *Env) serviceContainerID(ctx context.Context, service string) (string, error) {
	out, err := e.compose(ctx, nil, "ps", "-q", service)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("no container for service %q in project %s", service, e.Project)
	}
	// `ps -q` may emit multiple lines if scaled; take the first.
	return strings.Fields(id)[0], nil
}

// servicePort returns the host port mapped to a service's container port, e.g.
// "32769" from `docker compose port panel 8443` -> "127.0.0.1:32769".
func (e *Env) servicePort(ctx context.Context, service, containerPort string) (string, error) {
	var last error
	for i := 0; i < 30; i++ {
		out, err := e.compose(ctx, nil, "port", service, containerPort)
		if err == nil {
			line := strings.TrimSpace(out)
			if idx := strings.LastIndex(line, ":"); idx >= 0 && idx+1 < len(line) {
				return strings.TrimSpace(line[idx+1:]), nil
			}
			last = fmt.Errorf("unparseable port mapping %q", line)
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("could not resolve host port for %s:%s: %w", service, containerPort, last)
}

// ensureBucketReady makes the MinIO bucket if it is not already present, bounded
// so a slow MinIO start does not wedge the test.
func (e *Env) ensureBucketReady() error {
	return Poll(60*time.Second, time.Second, func() (bool, error) {
		if err := e.S3.EnsureBucket(); err != nil {
			return false, err
		}
		return true, nil
	})
}

// projectName builds a parallel-safe, compose-legal project name from the test
// name plus a random suffix, so two runs of the same test never collide.
func projectName(t *testing.T) string {
	base := strings.ToLower(t.Name())
	base = projSanitize.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) > 40 {
		base = base[:40]
	}
	return fmt.Sprintf("e2e-%s-%s", base, randSuffix())
}

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
