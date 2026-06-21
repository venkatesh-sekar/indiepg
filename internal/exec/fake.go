package exec

import (
	"context"
	"strings"
	"sync"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// FakeRunner is an in-memory Runner for tests. It records every RunSpec it
// receives and returns canned responses matched by a substring of the resolved
// argv. The zero value is usable; matching falls back to Default.
type FakeRunner struct {
	mu sync.Mutex

	// Default is returned when no responder matches.
	Default FakeResponse
	// dryRun is reported by DryRun and stamped onto results.
	dryRun bool

	responders []responder
	calls      []RunSpec
}

// FakeResponse is a canned result for a matched command.
type FakeResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Err, when set, is returned from Run (wrapped as a core exec error if it
	// is not already a *core.Error).
	Err error
}

type responder struct {
	match string // substring matched against the joined argv
	resp  FakeResponse
}

// NewFakeRunner builds a FakeRunner.
func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

// SetDryRun sets the dry-run flag reported by DryRun.
func (f *FakeRunner) SetDryRun(v bool) { f.dryRun = v }

// DryRun reports the configured dry-run flag.
func (f *FakeRunner) DryRun() bool { return f.dryRun }

// On registers a canned response for any command whose joined argv contains
// match. The most recently registered matching responder wins, so later calls
// can override earlier ones. Returns the receiver for chaining.
func (f *FakeRunner) On(match string, resp FakeResponse) *FakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responders = append(f.responders, responder{match: match, resp: resp})
	return f
}

// Run records spec and returns the matching canned response.
func (f *FakeRunner) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, spec)
	argv := resolveArgv(spec)
	joined := strings.Join(argv, " ")
	resp := f.Default
	// Iterate in reverse so the last matching responder wins.
	for i := len(f.responders) - 1; i >= 0; i-- {
		if strings.Contains(joined, f.responders[i].match) {
			resp = f.responders[i].resp
			break
		}
	}
	f.mu.Unlock()

	res := RunResult{
		Command:  argv,
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: resp.ExitCode,
		DryRun:   f.dryRun,
	}
	if resp.Err != nil {
		if _, ok := core.AsError(resp.Err); ok {
			return res, resp.Err
		}
		return res, core.ExecError("command failed: %s", joined).Wrap(resp.Err)
	}
	return res, nil
}

// Calls returns a copy of every RunSpec received, in order.
func (f *FakeRunner) Calls() []RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunSpec(nil), f.calls...)
}

// CallCount returns how many commands were run.
func (f *FakeRunner) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Reset clears recorded calls (responders are kept).
func (f *FakeRunner) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

var _ Runner = (*FakeRunner)(nil)
var _ Runner = (*OSRunner)(nil)
