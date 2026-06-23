#!/usr/bin/env bash
# indiepg Ralph loop — autonomous self-improvement runner.
#
# Each iteration invokes `claude` with the orchestrator prompt (PROMPT.md).
# The agent picks ONE item, implements it, gets it reviewed, verifies it, and
# ends with ONE atomic commit. This script enforces the guardrails around that:
# clean tree, atomicity, auto-revert of any mess, wall-clock + iteration caps.
#
# Philosophy (per the project owner):
#   - One thing at a time. Keep moving forward.
#   - NEVER park work for a human. The agent decides.
#   - If an iteration leaves a mess, just revert to the last good commit and
#     redo the work next time — don't flail, don't HALT.
#   - Stop only when (a) the agent judges the panel rock-solid (COMPLETE.md),
#     (b) a cap is hit, or (c) the genuinely-unrecoverable case (HALT.md).
#
# Usage:
#   ./scripts/ralph/ralph.sh                          # opus, 100 iters, 24h cap
#   ./scripts/ralph/ralph.sh --model sonnet 200       # custom model + max iters
#   ./scripts/ralph/ralph.sh --runtime-cap-hours 12   # custom wall-clock cap

set -u  # NOT -e: we handle errors ourselves.

# ---- defaults ----
MODEL="opus"
MAX_ITERATIONS=100
RUNTIME_CAP_HOURS=24
SLEEP_BETWEEN=2
# After this many consecutive iterations that produce NO commit, stop and ask
# for a human look — this is the only "stuck" backstop. Set high; should be rare.
NO_PROGRESS_LIMIT=10

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)               MODEL="$2"; shift 2 ;;
    --model=*)             MODEL="${1#*=}"; shift ;;
    --runtime-cap-hours)   RUNTIME_CAP_HOURS="$2"; shift 2 ;;
    --runtime-cap-hours=*) RUNTIME_CAP_HOURS="${1#*=}"; shift ;;
    --sleep)               SLEEP_BETWEEN="$2"; shift 2 ;;
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

# Cleanliness is judged on TRACKED files only. Untracked files (e.g. sandbox
# device-dotfiles, editor scratch) are intentionally ignored so they never
# trip the loop or get destroyed.
tree_is_clean() { git diff --quiet && git diff --cached --quiet; }

START_EPOCH=$(date +%s)
RUNTIME_CAP_SECONDS=$((RUNTIME_CAP_HOURS * 3600))

echo "==============================================================="
echo "  indiepg Ralph loop"
echo "  Model:          $MODEL"
echo "  Max iterations: $MAX_ITERATIONS"
echo "  Runtime cap:    ${RUNTIME_CAP_HOURS}h"
echo "  Working dir:    $PROJECT_ROOT"
echo "==============================================================="

# ---- pre-flight ----
if [[ -f "$HALT_FILE" ]]; then
  echo "[HALT] $HALT_FILE exists. Read it, resolve, and delete it before restarting."
  cat "$HALT_FILE"; exit 2
fi
if [[ -f "$COMPLETE_FILE" ]]; then
  echo "[COMPLETE] The loop already declared the work done. See $COMPLETE_FILE."
  echo "           Delete it to let the loop keep improving."
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

for i in $(seq 1 "$MAX_ITERATIONS"); do
  NOW_EPOCH=$(date +%s); ELAPSED=$((NOW_EPOCH - START_EPOCH))

  if (( ELAPSED >= RUNTIME_CAP_SECONDS )); then
    echo; echo "[CAP] Hit ${RUNTIME_CAP_HOURS}h runtime cap after $((i-1)) iterations. Clean stop."
    echo "      Restart the script to keep going."
    exit 0
  fi
  if [[ -f "$HALT_FILE" ]]; then
    echo; echo "[HALT] Orchestrator wrote HALT.md. Stopping."; cat "$HALT_FILE"; exit 1
  fi
  if [[ -f "$COMPLETE_FILE" ]]; then
    echo; echo "[COMPLETE] Orchestrator declared the panel done. Stopping."; cat "$COMPLETE_FILE"; exit 0
  fi

  # Self-heal: if the previous iteration left tracked changes uncommitted, it
  # broke atomicity. Don't trust a half-done change — revert to the last good
  # commit and let this iteration redo the work cleanly.
  if ! tree_is_clean; then
    echo "[heal] Tracked tree dirty before iteration $i — reverting to last commit and redoing."
    git reset --hard HEAD >/dev/null 2>&1
  fi

  echo; echo "==============================================================="
  echo "  Iteration $i / $MAX_ITERATIONS   —   elapsed ${ELAPSED}s"
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
(empty backlog it won't extend, a verify gate it can't pass, or a prompt issue).
Look at the recent run-logs/, decide, then delete this file to resume.
EOF
      echo "[HALT] $NO_PROGRESS_LIMIT iterations with no progress. Wrote HALT.md."; exit 1
    fi
  fi

  sleep "$SLEEP_BETWEEN"
done

echo; echo "==============================================================="
echo "  Reached max iterations ($MAX_ITERATIONS). Normal stop — restart to continue."
echo "==============================================================="
exit 0
