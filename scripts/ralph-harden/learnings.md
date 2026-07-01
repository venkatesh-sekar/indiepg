# Learnings — hardening loop

Durable memory across iterations. Newest at top. Keep entries one or two lines.
When a pattern recurs (≥2×), promote it to a short rule here so future
iterations don't rediscover it.

## Active rules

- A FakeRunner that matches on an argv SUBSTRING (`internal/exec/fake.go`) returns
  canned stdout DECOUPLED from the SQL/query text that follows the matched flag. So a
  test that pins a helper's OUTPUT (parsed rows) can't catch a mutation to the QUERY
  itself — dropping/inverting a `WHERE` predicate keeps the same canned stdout and stays
  GREEN. When the query text IS the contract (e.g. `SELECT extname FROM pg_extension
  WHERE extname <> 'plpgsql'` — inverting to `= 'plpgsql'` silently hides every real
  extension from a parity/upgrade blocker), pin the predicate on the recorded argv:
  iterate `fake.Calls()`, `strings.Join(c.Args, " ")`, and `require.Contains(joined,
  "<exact predicate>")` — asserting the exact `<>` form catches BOTH drop-WHERE and
  invert-to-`=`. Counting the matching calls also pins fan-out (one query per database),
  killing a skip-a-database mutation. This is the SQL analogue of the equality-gate rule:
  when the FakeRunner can't execute the filter, assert the filter is present in what was
  sent. (Iter #13, test-skeptic found the SQL-decoupled escape.)
- When perl-mutating a source line to prove a test, GREP the file for that exact line
  first — the same idiom often appears in TWO sibling helpers (e.g. the blank-line guard
  `if line = strings.TrimSpace(line); line != "" {` lives in BOTH `listDatabaseNames` and
  `installedExtensions`). `perl -0pi -e s///` without `/g` hits only the FIRST occurrence,
  which may be the copy your test doesn't exercise → a false GREEN that looks like a weak
  test but is really a mis-aimed mutation. Anchor the substitution on a unique neighbor
  line (e.g. `seen[line] = true` vs `names = append(...)`) to target the intended copy.
  (Iter #13)

- A match anchored to the END of a whitespace-token (`strings.HasSuffix(field, ":"+port)`
  over `strings.Fields(line)`) needs TWO kinds of near-miss to be fully pinned, not one.
  (1) A leading-boundary near-miss — a longer port whose digits *contain but don't equal*
  the target (`:15432` vs `:5432`) — kills a "dropped the leading colon" mutation
  (`needle := port`). (2) An INTERIOR-substring near-miss — the target digits appearing
  mid-token, e.g. a high port that *starts* with them (`:54321`, so the raw line contains
  `:5432`) or an address literal that embeds them (`[fe80::5432]:22`) — kills a
  `HasSuffix(token)`→`Contains(whole_line)` rewrite (drops the field anchoring). The first
  near-miss alone leaves the second mutation GREEN because `:5432` isn't a substring of
  `:15432` at all. Generalizes the equality-gate rule: probe the values only the *looser*
  predicate admits, and do it from BOTH the leading and trailing side of the anchor.
  (Iter #12, test-skeptic found the `Contains(line)` escape.)
- A typed-confirm gate makes `send(typed)` and `send(expected)` OBSERVABLY IDENTICAL
  on every reachable path — don't chase a mutation that swaps one for the other. When
  the confirm button is `disabled` unless `typed === name`, `install()` can only fire
  with `typed === name`, so `confirm: typed` vs `confirm: name` emit the same wire
  value 100% of the time. A test-skeptic will flag `confirm: typed`→`confirm: name` as
  a "passing mutation," but it's a behavior-preserving REFACTOR from the client's view,
  not a defect, and no UI-driven test can distinguish them without pinning implementation
  detail. Verify the real backstop instead: the SERVER re-validates (`core.RequireConfirmation`
  → `Confirmation.OK()` is exact `Typed == Expected`, `extensions.go:292`), so the wire
  token is independently enforced server-side — the client gate is dumb-proofing, not the
  security boundary. Reject the finding after verifying (don't blindly implement); cover
  the server re-check with a Go test if you want that invariant pinned. (Iter #11, test-skeptic)
- An equality gate — a DB `CHECK (col = K)` OR a UI typed-confirm (`typed.trim() ===
  expected`) — is under-tested by exact-wrong + exact-right values alone. A one-line
  weakening to a LOOSER predicate (`>= 1`, `id*id=1`, `.includes`/`.startsWith`,
  `Number(typed) === n`) still rejects the exact-wrong value AND accepts the
  exact-right one, so it stays green. Probe the values that only the loosened forms
  admit: negatives/squares for numeric CHECKs; a SUPERSTRING (`"169"` vs `"16"`) and
  a NUMERIC-EQUIVALENT non-exact spelling (`"16.0"`) for typed-confirm gates — each
  must keep the gate CLOSED. Also cross-wire test: when two sibling gates expect
  DIFFERENT tokens (finalize wants the OLD major, rollback the NEW), make the two
  distinct and assert each rejects the other's token, which kills a from↔to swap.
  (Iter #9 DB CHECK, Iter #10 UI confirm — test-skeptic found the escape both times.)
- When testing a value-pinning constraint (`CHECK (col = K)`), a positive-and-zero
  probe set is NOT enough — probe NEGATIVE / algebraically-equivalent values too. A
  one-line weakening like `= 1` → `id * id = 1` or `abs(id) = 1` still rejects 0 and
  2 but ADMITS -1, so an id∈{0,2}-only test stays green while a second, diverging
  singleton row becomes insertable. Include the boundary values that satisfy the
  weakened forms (negatives, squares) so such a mutation reds the test. Also assert
  the SPECIFIC failure (`ErrorContains "CHECK constraint failed"`), not just
  `require.Error`, so a stray NOT NULL/type failure can't pass for the wrong reason;
  pair it with a positive control (the pinned value is accepted) to prove the row is
  otherwise valid. (Iter #9, test-skeptic)
- Errors that wrap a net/http failure LEAK the request URL: both
  `http.NewRequestWithContext` (via url.Parse) and `http.Client.Do` return a
  `*url.Error` whose `.Error()` embeds the full URL. When the URL is secret-bearing
  (a webhook URL may embed a Slack/Discord/n8n token in its path), never `.Wrap()`
  that error and never interpolate the URL — return a redaction-safe message + hint
  instead. Pushover-style URLs that are a fixed public constant with the token in
  the form body are safe to wrap. (Iter #3)
- When a test asserts an error does NOT leak a secret, check the FULL
  operator-visible surface, not just `err.Error()`. `core.Error.Error()` renders
  only `Code: Message[: cause]`, but `server.toAPIError` (respond.go:122-125)
  serializes `Message`, `Hint`, AND `Details` onto the API wire — so a leak
  reintroduced via `WithHint(...)`/`WithDetail(...)` passes an `err.Error()`-only
  assertion. Assert across message + `.Hint` + `.Details`. (Iter #3, test-skeptic)
- Before assuming a backlog item is open, GREP for its covering test — much of this
  tree is already well-tested. Iter #3's audit found auth/session, login-lockout,
  config atomic-write, config self-heal, migrate verification, and S3 ownership all
  already covered; writing another test there would be tautology theater. Re-seed
  (Mode S) when the top items are stale-because-covered, and mark them Done with the
  covering test names so the next iteration doesn't re-audit them. (Iter #3)
- SQL-rewriting/classification guards must handle EVERY syntactic form of a
  construct, not just the common keyword. A top-level row bound is `LIMIT` *or*
  `FETCH FIRST/NEXT ... ROWS` (PostgreSQL rejects both in one query), so an
  auto-LIMIT gate keyed on LIMIT alone breaks a valid FETCH read. When testing
  such a guard, cover lower/mixed-case and quoted-identifier variants: a
  case-sensitive or keyword-only match is a plausible one-line regression the
  uppercase-only test won't catch (the test-skeptic found exactly this). (Iter #2)
- Run `gofmt -l $(git ls-files '*.go')` FIRST, before picking work. The committed
  tree can carry gofmt drift independent of your change (doc-comment list reflow,
  struct-field alignment under gofmt ≥1.19) that reds the fmt gate. It's a
  priority-0 red gate — fix it (a plain `gofmt -w`, no behavior change) as its own
  small commit. Seen Iter #1: server.go + 2 e2e files were drifted at HEAD.
- `make` here is an unresolved zsh autoload stub — run the explicit gate commands
  from AGENTS.md directly (gofmt -l / go vet / go test ./... / CGO build). Build to
  /tmp (`go build -o /tmp/...`) so no stray binary dirties the tree.
- Preflights that gate recovery/restore should fail OPEN on uncertainty (can't
  enumerate the repo, missing metadata) and reject ONLY the provably-impossible
  case. Blocking recovery — the most data-critical op — on a transient read is
  worse than a late pgBackRest error. pgBackRest stays the final arbiter. (Iter #1)
- Prefer make targets: `make verify` (backend gate) and `make verify-web` (web
  gate). They encode the right flags.
- A test only counts if it would **fail** when the behavior it protects breaks.
  If a one-line source mutation wouldn't fail the test, the test is too weak.
- Sandbox: `go`/`make` can be blocked by snap-confine (`cap_dac_override`). Run
  them outside the sandbox; never commit unverified.
- Shared defaults are `scripts/ralph/DEFAULTS.md`; real behavior reference is the
  `sm` CLI at `/primary01/git/server-management/src/sm/`.
- e2e (`test/e2e`, `//go:build e2e`) is Docker/systemd-based and heavy; run one
  scenario with `make e2e SCENARIO=TestName`. Skip if Docker is unavailable, but
  say so in the progress note.

## Rejected ideas — do not re-propose

- pg/hba `injectHBARules` "self-heal a widened managed block" (normalize a
  marker-present-but-widened block back to loopback+socket-only) — the fix would
  REVERT a documented operator hardening: `hba.go:26` says an operator sharing the
  host may replace the managed `trust` lines with `scram-sha-256`, and the current
  presence-only behavior IS that escape hatch. Blindly re-normalizing turns scram
  back into trust — a *widening* — violating the security tie-break. A "heal only
  widenings, keep hardenings" variant needs semantic pg_hba permissiveness parsing
  (trust vs scram, CIDR ranges): complex, error-prone, YAGNI, and getting it wrong
  is itself a security risk. And a widened managed block requires root/postgres
  write to the 0600 hba file — the actor already owns the box, so it's not an
  escalation. (Iter #6)

- restore preflight "free disk + inodes" (for the LIVE PITR restore) — the live
  restore replaces the existing data dir in place (pgBackRest --delta / full over
  the current PGDATA), so it needs no extra headroom beyond what the cluster already
  occupies. The deep restore-TEST, which writes into a fresh scratch dir, already
  gates on `deepHeadroomFactor × db size` free (`restore_deep.go`, tested via the
  injectable `diskFree`). A disk precheck on the live restore adds a false-negative
  risk to the most data-critical op for no real benefit. (Iter #3)
