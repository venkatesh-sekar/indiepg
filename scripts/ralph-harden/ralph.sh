#!/usr/bin/env bash
# indiepg hardening loop — autonomous "make it rock-solid" runner.
#
# Each iteration invokes `claude` with the orchestrator prompt (PROMPT.md).
# The agent picks ONE hardening item — a missing test that proves vibe-coded
# code actually does what it claims, a missing preflight check, a silent
# failure turned into a loud one, or a weak default tightened — implements it,
# gets it reviewed, verifies it (make verify), and ends with ONE atomic commit.
# This script enforces the guardrails around that: clean tree, atomicity,
# auto-revert of any mess, wall-clock + iteration caps (unless --forever).
#
# Philosophy (per the project owner):
#   - One thing at a time. Keep moving forward. Fail fast, loudly, clearly.
#   - NEVER park work for a human. The agent decides.
#   - If an iteration leaves a mess, revert to the last good commit and redo
#     the work next time — don't flail, don't HALT.
#   - This loop is NEVER-ENDING: it does not declare the panel "done". When the
#     backlog runs thin it re-audits and finds more to harden. It stops only on
#     a cap, a genuine no-progress stall (HALT.md), or Ctrl-C.
#
# Usage:
#   ./scripts/ralph-harden/ralph.sh                       # opus, 500 iters, 24h cap
#   ./scripts/ralph-harden/ralph.sh --forever             # unbounded: no iter/time cap
#   ./scripts/ralph-harden/ralph.sh --model sonnet 200    # custom model + max iters
#   ./scripts/ralph-harden/ralph.sh --runtime-cap-hours 12

set -u  # NOT -e: we handle errors ourselves.

# ---- defaults ----
MODEL="opus"
MAX_ITERATIONS=500
RUNTIME_CAP_HOURS=24
SLEEP_BETWEEN=2
FOREVER=0
# After this many consecutive iterations that produce NO commit, stop and ask
# for a human look — the only "stuck" backstop. Even in --forever this holds,
# so a wedged loop can't burn indefinitely with zero output.
NO_PROGRESS_LIMIT=10

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)               MODEL="$2"; shift 2 ;;
    --model=*)             MODEL="${1#*=}"; shift ;;
    --runtime-cap-hours)   RUNTIME_CAP_HOURS="$2"; shift 2 ;;
    --runtime-cap-hours=*) RUNTIME_CAP_HOURS="${1#*=}"; shift ;;
    --sleep)               SLEEP_BETWEEN="$2"; shift 2 ;;
    --forever)             FOREVER=1; shift ;;
    *) [[ "$1" =~ ^[0-9]+$ ]] && MAX_ITERATIONS="$1"; shift ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PROMPT_FILE="$SCRIPT_DIR/PROMPT.md"
HALT_FILE="$SCRIPT_DIR/HALT.md"
COMPLETE_FILE="$SCRIPT_DIR/COMPLETE.md"
RUN_LOG_DIR="$SCRIPT_DIR/run-logs"

mkdir -p "$RUN_LOG_DIR"
cd "$PROJECT_ROOT"

# Keep npm/npx off the read-only ~/.npm cache the sandbox exposes — use a
# project-local, writable cache so web tooling works inside the loop.
export NPM_CONFIG_CACHE="$PROJECT_ROOT/web/.npm-cache"

# Cleanliness is judged on TRACKED files only. Untracked files (e.g. sandbox
# device-dotfiles, editor scratch) are intentionally ignored so they never
# trip the loop or get destroyed.
tree_is_clean() { git diff --quiet && git diff --cached --quiet; }

START_EPOCH=$(date +%s)
RUNTIME_CAP_SECONDS=$((RUNTIME_CAP_HOURS * 3600))

echo "==============================================================="
echo "  indiepg hardening loop (ralph-harden)"
echo "  Model:          $MODEL"
if (( FOREVER )); then
  echo "  Max iterations: ∞ (--forever)"
  echo "  Runtime cap:    none (--forever)"
else
  echo "  Max iterations: $MAX_ITERATIONS"
  echo "  Runtime cap:    ${RUNTIME_CAP_HOURS}h"
fi
echo "  Working dir:    $PROJECT_ROOT"
echo "==============================================================="

# ---- pre-flight ----
if [[ -f "$HALT_FILE" ]]; then
  echo "[HALT] $HALT_FILE exists. Read it, resolve, and delete it before restarting."
  cat "$HALT_FILE"; exit 2
fi
if [[ -f "$COMPLETE_FILE" ]]; then
  echo "[COMPLETE] A previous run wrote COMPLETE.md. This loop is meant to be"
  echo "           never-ending — read it, then delete it to keep hardening."
  cat "$COMPLETE_FILE"; exit 0
fi
if [[ ! -f "$PROMPT_FILE" ]]; then
  echo "[ERROR] Missing orchestrator prompt: $PROMPT_FILE"; exit 2
fi
# Cold-start protects the operator's own uncommitted work.
if ! tree_is_clean; then
  echo "[ERROR] Tracked working tree is dirty at cold start. Commit or stash first."
  git status --short; exit 2
fi
if ! command -v claude >/dev/null 2>&1; then
  echo "[ERROR] 'claude' CLI not found on PATH."; exit 2
fi

CONSEC_NO_COMMIT=0
i=0

while :; do
  i=$((i + 1))
  if (( FOREVER == 0 )) && (( i > MAX_ITERATIONS )); then
    echo; echo "==============================================================="
    echo "  Reached max iterations ($MAX_ITERATIONS). Normal stop — restart to continue."
    echo "==============================================================="
    exit 0
  fi

  NOW_EPOCH=$(date +%s); ELAPSED=$((NOW_EPOCH - START_EPOCH))

  if (( FOREVER == 0 )) && (( ELAPSED >= RUNTIME_CAP_SECONDS )); then
    echo; echo "[CAP] Hit ${RUNTIME_CAP_HOURS}h runtime cap after $((i-1)) iterations. Clean stop."
    echo "      Restart the script (or use --forever) to keep going."
    exit 0
  fi
  if [[ -f "$HALT_FILE" ]]; then
    echo; echo "[HALT] Orchestrator wrote HALT.md. Stopping."; cat "$HALT_FILE"; exit 1
  fi
  if [[ -f "$COMPLETE_FILE" ]]; then
    echo; echo "[COMPLETE] Orchestrator wrote COMPLETE.md. Stopping."; cat "$COMPLETE_FILE"; exit 0
  fi

  # Self-heal: if the previous iteration left tracked changes uncommitted, it
  # broke atomicity. Don't trust a half-done change — revert to the last good
  # commit and let this iteration redo the work cleanly.
  if ! tree_is_clean; then
    echo "[heal] Tracked tree dirty before iteration $i — reverting to last commit and redoing."
    git reset --hard HEAD >/dev/null 2>&1
  fi

  echo; echo "==============================================================="
  if (( FOREVER )); then
    echo "  Iteration $i   —   elapsed ${ELAPSED}s   —   ∞ mode"
  else
    echo "  Iteration $i / $MAX_ITERATIONS   —   elapsed ${ELAPSED}s"
  fi
  echo "==============================================================="

  ITER_LOG="$RUN_LOG_DIR/iter-$(printf '%04d' "$i").log"
  COMMITS_BEFORE=$(git rev-list --count HEAD)

  claude \
    --dangerously-skip-permissions \
    --print \
    --model "$MODEL" \
    < "$PROMPT_FILE" \
    2>&1 | tee "$ITER_LOG"
  CLAUDE_EXIT=${PIPESTATUS[0]}

  COMMITS_AFTER=$(git rev-list --count HEAD)
  COMMIT_DELTA=$((COMMITS_AFTER - COMMITS_BEFORE))
  echo; echo "[iter $i] claude exit=$CLAUDE_EXIT, commits added=$COMMIT_DELTA"

  # Enforce atomicity: nothing uncommitted may survive an iteration.
  if ! tree_is_clean; then
    echo "[heal] Iteration $i left uncommitted changes — reverting them (atomicity)."
    git reset --hard HEAD >/dev/null 2>&1
  fi

  if (( COMMIT_DELTA > 0 )); then
    CONSEC_NO_COMMIT=0
  else
    CONSEC_NO_COMMIT=$((CONSEC_NO_COMMIT + 1))
    echo "[iter $i] no commit produced ($CONSEC_NO_COMMIT in a row)."
    if (( CONSEC_NO_COMMIT >= NO_PROGRESS_LIMIT )); then
      cat > "$HALT_FILE" <<EOF
# HALT — no progress

$NO_PROGRESS_LIMIT iterations in a row produced no commit. The loop is stuck
(a verify gate it can't pass, an audit that keeps finding nothing to seed, or a
prompt issue). Look at the recent run-logs/, decide, then delete this file to
resume.
EOF
      echo "[HALT] $NO_PROGRESS_LIMIT iterations with no progress. Wrote HALT.md."; exit 1
    fi
  fi

  sleep "$SLEEP_BETWEEN"
done
