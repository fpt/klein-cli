#!/usr/bin/env bash
# Stop hook for the klein-cli Go project.
#
# After Claude finishes a turn:
#   1. gofmt -s -w the .go files Claude changed (vs HEAD, plus untracked) —
#      NOT the whole repo, so it never reformats unrelated legacy files.
#   2. Run golangci-lint on NEWLY introduced issues only (--new-from-rev=HEAD),
#      so the project's large pre-existing baseline is ignored.
#
# If new lint issues are found, block the stop and feed them back so Claude
# fixes them. A loop guard (stop_hook_active) prevents infinite retries.
set -uo pipefail

input=$(cat)

root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$root" || exit 0

# 1. Format only the .go files Claude touched.
{ git diff --name-only HEAD -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } |
	sort -u |
	while IFS= read -r f; do
		[ -n "$f" ] && [ -f "$f" ] && gofmt -s -w "$f" >/dev/null 2>&1
	done

# 2. Lint only newly introduced issues; the pre-existing baseline is ignored.
command -v golangci-lint >/dev/null 2>&1 || exit 0
if out=$(golangci-lint run --new-from-rev=HEAD ./... 2>&1); then
	exit 0 # no new issues — allow the stop
fi

# jq is required to emit the structured block + feed findings back. If it's
# missing (e.g. on another machine), surface the issues on stderr and allow the
# stop rather than blocking with no way to communicate them.
if ! command -v jq >/dev/null 2>&1; then
	printf 'go-format-lint: new golangci-lint issues (install jq to auto-feed them back to Claude):\n%s\n' "$out" >&2
	exit 0
fi

# Loop guard: if we already blocked once in this chain, report to stderr and
# allow the stop instead of retrying forever.
if [ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = "true" ]; then
	printf 'go-format-lint: unresolved new golangci-lint issues:\n%s\n' "$out" >&2
	exit 0
fi

# Block the stop and hand the findings to Claude to fix.
printf '%s' "$out" |
	jq -Rs '{decision:"block", reason: ("golangci-lint reported newly introduced issues. Please fix them, then finish:\n\n" + .)}'
