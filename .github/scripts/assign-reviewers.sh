#!/usr/bin/env bash
#
# assign-reviewers.sh -- the deterministic "hands" of the LLM-assisted reviewer
# router (see .github/workflows/assign-reviewers.yml for the full design).
#
# ---------------------------------------------------------------------------
# Security model: brain / hands split
# ---------------------------------------------------------------------------
# Claude (the "brain") runs with NO GitHub token and NO write powers. It only
# emits a JSON decision file. THIS script (the "hands") is the only component
# that holds GITHUB_TOKEN and talks to the API. Because of that split, the
# trust boundary lives here:
#
#   * We never trust Claude's chosen logins blindly. We build an ALLOWLIST of
#     handles by extracting every @handle that literally appears in
#     .github/REVIEWERS.md, and we discard any login from Claude's output that
#     is not on that allowlist. Prompt injection in the PR diff therefore
#     cannot cause us to act on an arbitrary login -- the worst case is a
#     reviewer the doc already lists.
#   * We never echo Claude's raw decision text into a comment or the log; only
#     validated, allowlisted handles and the matched rationale strings.
#
# Inputs are read entirely from environment variables so the script is testable
# in isolation (run it with the env set and a hand-written DECISION_FILE):
#   GH_TOKEN        - GitHub token (the action passes the ephemeral one)
#   REPO            - owner/name, e.g. stacklok/toolhive
#   PR_NUMBER       - pull request number
#   PR_AUTHOR       - PR author login (removed from required; GitHub rejects it)
#   REVIEWERS_FILE  - path to the ownership doc (default .github/REVIEWERS.md)
#   DECISION_FILE   - path to Claude's JSON decision (default /tmp/reviewer-decision.json)
#
# Style mirrors .github/workflows/retest.yaml: set -euo pipefail, hardened
# `gh api` + `jq`, no third-party action holds the token.

set -euo pipefail

REPO="${REPO:?REPO must be set (owner/name)}"
PR_NUMBER="${PR_NUMBER:?PR_NUMBER must be set}"
PR_AUTHOR="${PR_AUTHOR:-}"
REVIEWERS_FILE="${REVIEWERS_FILE:-.github/REVIEWERS.md}"
DECISION_FILE="${DECISION_FILE:-/tmp/reviewer-decision.json}"

MARKER="<!-- reviewer-router -->"

if [ ! -f "$REVIEWERS_FILE" ]; then
  echo "::error::Reviewers file not found: $REVIEWERS_FILE"
  exit 1
fi
if [ ! -s "$DECISION_FILE" ]; then
  echo "No decision file at $DECISION_FILE; nothing to do."
  exit 0
fi
if ! jq -e . "$DECISION_FILE" >/dev/null 2>&1; then
  echo "::error::Decision file is not valid JSON; refusing to act."
  exit 1
fi

# ---------------------------------------------------------------------------
# 1. Build the ALLOWLIST from REVIEWERS.md.
#    Contract: a reviewer is any @handle token in the doc. We strip the @,
#    lowercase for case-insensitive matching, and dedupe. GitHub logins are
#    [A-Za-z0-9-] (no leading/trailing hyphen, but we keep the regex simple
#    and permissive -- the allowlist only ever shrinks the candidate set).
# ---------------------------------------------------------------------------
mapfile -t allowlist < <(
  grep -oE '@[A-Za-z0-9][A-Za-z0-9-]*' "$REVIEWERS_FILE" \
    | sed 's/^@//' \
    | tr '[:upper:]' '[:lower:]' \
    | sort -u
)

if [ "${#allowlist[@]}" -eq 0 ]; then
  echo "::warning::No @handles found in $REVIEWERS_FILE; nothing to route."
  exit 0
fi
echo "Allowlist (${#allowlist[@]}): ${allowlist[*]}"

# is_allowed <handle-lowercased> -> 0 if on allowlist
is_allowed() {
  local needle="$1" h
  for h in "${allowlist[@]}"; do
    [ "$h" = "$needle" ] && return 0
  done
  return 1
}

# canonical_handle <handle-lowercased> -> prints the original-cased handle from
# REVIEWERS.md (GitHub is case-insensitive on logins, but we mention the form
# the doc uses for readability).
canonical_handle() {
  local needle="$1"
  grep -oE '@[A-Za-z0-9][A-Za-z0-9-]*' "$REVIEWERS_FILE" \
    | sed 's/^@//' \
    | awk -v n="$needle" 'tolower($0)==n {print; exit}'
}

# ---------------------------------------------------------------------------
# 2. Read Claude's lists, lowercase + dedupe, validate against allowlist, and
#    drop the PR author from the required set (GitHub rejects requesting the
#    author as a reviewer).
# ---------------------------------------------------------------------------
author_lc=$(printf '%s' "$PR_AUTHOR" | tr '[:upper:]' '[:lower:]')

read_list() { # read_list <jq-path> -> newline list, lowercased, deduped
  jq -r "(.${1} // []) | .[]? | select(type==\"string\")" "$DECISION_FILE" \
    | tr '[:upper:]' '[:lower:]' \
    | sed 's/^@//' \
    | grep -E '^[A-Za-z0-9][A-Za-z0-9-]*$' \
    | sort -u || true
}

validate() { # validate <newline-list> [--drop-author] -> allowlisted handles
  local drop_author="${2:-}" h
  while IFS= read -r h; do
    [ -z "$h" ] && continue
    if ! is_allowed "$h"; then
      # Log to stderr so diagnostics never leak into the captured handle list.
      echo "::warning::Discarding '$h' -- not on the REVIEWERS.md allowlist." >&2
      continue
    fi
    if [ "$drop_author" = "--drop-author" ] && [ "$h" = "$author_lc" ]; then
      echo "Dropping PR author '$h' from required reviewers." >&2
      continue
    fi
    printf '%s\n' "$h"
  done <<<"$1"
}

required_lc=$(validate "$(read_list required)" --drop-author | sort -u)
notify_lc=$(validate "$(read_list notify)" | sort -u)

# notify and required must be disjoint -- if Claude listed someone in both,
# keep them only as required.
if [ -n "$required_lc" ]; then
  notify_lc=$(comm -23 <(printf '%s\n' "$notify_lc" | sort -u) \
                       <(printf '%s\n' "$required_lc" | sort -u) || true)
fi

mapfile -t required <<<"$(printf '%s\n' "$required_lc" | grep -v '^$' || true)"
mapfile -t notify   <<<"$(printf '%s\n' "$notify_lc"   | grep -v '^$' || true)"

echo "Required (validated): ${required[*]:-<none>}"
echo "Notify   (validated): ${notify[*]:-<none>}"

# ---------------------------------------------------------------------------
# 3. Request the required reviewers (idempotent). Skip anyone who has already
#    submitted a review -- re-requesting them would be noise.
# ---------------------------------------------------------------------------
# Logins that have already submitted a review on this PR (lowercased).
already_reviewed=$(
  gh api "repos/$REPO/pulls/$PR_NUMBER/reviews?per_page=100" \
    --jq '.[].user.login' 2>/dev/null \
    | tr '[:upper:]' '[:lower:]' | sort -u || true
)

to_request=()
for h in "${required[@]:-}"; do
  [ -z "$h" ] && continue
  if printf '%s\n' "$already_reviewed" | grep -qx "$h"; then
    echo "Skipping '$h' -- already submitted a review."
    continue
  fi
  # Use the canonical casing from the doc (falls back to lowercased handle).
  to_request+=("$(canonical_handle "$h" || printf '%s' "$h")")
done

if [ "${#to_request[@]}" -gt 0 ]; then
  # Build a JSON array of reviewer logins for the API call.
  reviewers_json=$(printf '%s\n' "${to_request[@]}" | jq -R . | jq -cs .)
  echo "Requesting reviewers: $reviewers_json"
  # Idempotent: GitHub no-ops on already-requested reviewers. Tolerate a 422
  # (e.g. a login that cannot be requested) without failing the whole job.
  if ! gh api -X POST "repos/$REPO/pulls/$PR_NUMBER/requested_reviewers" \
        --input - <<<"{\"reviewers\": $reviewers_json}" >/dev/null 2>&1; then
    echo "::warning::Reviewer request returned a non-success status (some logins may be non-requestable)."
  fi
else
  echo "No new reviewers to request."
fi

# ---------------------------------------------------------------------------
# 4. Upsert ONE marker comment: @-mention the notify tier and list the
#    validated rationale. Find by marker; update on synchronize instead of
#    posting a fresh comment each push.
# ---------------------------------------------------------------------------
# Build the comment body. Only validated handles + matched rationale text are
# ever rendered -- never Claude's raw JSON.
{
  printf '%s\n' "$MARKER"
  printf '### Suggested reviewers\n\n'

  if [ "${#required[@]}" -gt 0 ] && [ -n "${required[0]:-}" ]; then
    printf '**Required:** '
    sep=""
    for h in "${required[@]}"; do
      [ -z "$h" ] && continue
      printf '%s@%s' "$sep" "$(canonical_handle "$h" || printf '%s' "$h")"
      sep=", "
    done
    printf '\n\n'
  fi

  if [ "${#notify[@]}" -gt 0 ] && [ -n "${notify[0]:-}" ]; then
    printf '**FYI / for awareness:** '
    sep=""
    for h in "${notify[@]}"; do
      [ -z "$h" ] && continue
      printf '%s@%s' "$sep" "$(canonical_handle "$h" || printf '%s' "$h")"
      sep=", "
    done
    printf '\n\n'
  fi

  # Rationale: only for handles that survived validation (required or notify).
  printf '<details><summary>Why these reviewers</summary>\n\n'
  jq -r '(.rationale // [])[] | select(type=="object") |
         "\(.reviewer // "")\t\(.why // "")"' "$DECISION_FILE" \
    | while IFS=$'\t' read -r rv why; do
        [ -z "$rv" ] && continue
        rv_lc=$(printf '%s' "$rv" | sed 's/^@//' | tr '[:upper:]' '[:lower:]')
        if is_allowed "$rv_lc"; then
          # Strip any markdown/control noise from the rationale before printing.
          why_clean=$(printf '%s' "$why" | tr -d '\r' | tr '\n' ' ')
          printf -- '- @%s: %s\n' "$(canonical_handle "$rv_lc" || printf '%s' "$rv_lc")" "$why_clean"
        fi
      done
  printf '\n</details>\n\n'
  printf -- '_Reviewer suggestions are advisory; CODEOWNERS still governs required approvals._\n'
} > /tmp/reviewer-comment.md

body=$(cat /tmp/reviewer-comment.md)

# Find an existing marker comment to update.
existing_id=$(
  gh api "repos/$REPO/issues/$PR_NUMBER/comments?per_page=100" \
    --jq "map(select(.body | contains(\"$MARKER\"))) | .[0].id // empty" \
    2>/dev/null || true
)

if [ -n "$existing_id" ]; then
  echo "Updating existing reviewer-router comment ($existing_id)."
  gh api -X PATCH "repos/$REPO/issues/comments/$existing_id" \
    -f body="$body" >/dev/null
else
  echo "Posting new reviewer-router comment."
  gh api -X POST "repos/$REPO/issues/$PR_NUMBER/comments" \
    -f body="$body" >/dev/null
fi

echo "Done."
