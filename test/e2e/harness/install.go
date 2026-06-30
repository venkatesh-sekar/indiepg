//go:build e2e

package harness

import (
	"fmt"
	"regexp"
)

// adminPwRe captures the one-time admin password from `indiepg install`'s summary
// block:  "  Admin password  <pw>   (shown once — save it now)".
var adminPwRe = regexp.MustCompile(`Admin password\s+(\S+)\s+\(shown once`)

// ParseAdminPassword extracts the generated admin password from `indiepg install`
// output (the summary line printed exactly once). It errors if no generated
// password is present (e.g. install was given --password, or kept an existing one).
func ParseAdminPassword(installOutput string) (string, error) {
	m := adminPwRe.FindStringSubmatch(installOutput)
	if len(m) < 2 {
		return "", fmt.Errorf("no generated admin password found in install output")
	}
	return m[1], nil
}

// Install runs `indiepg install` (plus any extra args) inside the panel container
// and returns its combined stdout — from which ParseAdminPassword scrapes the
// one-time password. It is used by the install-from-scratch scenario on the base
// image. Provisioning takes minutes, so this uses an extended timeout.
func (e *Env) Install(extraArgs ...string) (string, error) {
	argv := append([]string{"indiepg", "install"}, extraArgs...)
	stdout, stderr, err := e.ExecCapture(argv...)
	if err != nil {
		return stdout + stderr, fmt.Errorf("indiepg install failed: %w\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return stdout, nil
}
