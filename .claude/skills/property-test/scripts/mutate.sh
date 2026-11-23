#!/usr/bin/env bash
# Plant bugs (mutations) one at a time and report which tests catch each one.
#
# usage: mutate.sh <mutations.tsv> <label>=<test command> [<label>=<test command> ...]
#
# mutations.tsv: one mutation per line, "name<TAB>file<TAB>perl substitution".
# Lines starting with # are ignored. perl runs with -0, so a substitution may
# span lines with \n. A test command that exits non-zero has caught the
# mutation; a build error is reported separately.
#
# Works on a dirty tree: each target file is backed up before a mutation and
# restored after it (also on interrupt). Run from the repository root.
set -euo pipefail

if [ $# -lt 2 ]; then
  sed -n '2,12p' "$0"
  exit 2
fi
tsv="$1"; shift
labels=() commands=()
for spec in "$@"; do
  labels+=("${spec%%=*}")
  commands+=("${spec#*=}")
done

backup="$(mktemp)"
current=""
cleanup() {
  if [ -n "$current" ]; then cp "$backup" "$current"; fi
  rm -f "$backup"
  # rapid repro files describe the mutated code, not the real one
  find . -path '*/testdata/rapid' -type d -prune -exec rm -rf {} + 2>/dev/null || true
  find . -type d -name testdata -empty -delete 2>/dev/null || true
}
trap cleanup EXIT INT TERM

row() {
  local name="$1" cell="$2"
  printf '| %s |' "$name"
  for _ in "${labels[@]}"; do printf ' %s |' "$cell"; done
  printf '\n'
}

printf '| mutation |'; for l in "${labels[@]}"; do printf ' %s |' "$l"; done; printf '\n'
printf '|---|'; for _ in "${labels[@]}"; do printf -- '---|'; done; printf '\n'

while IFS=$'\t' read -r name file subst; do
  [ -z "${name:-}" ] && continue
  case "$name" in \#*) continue ;; esac
  cp "$file" "$backup"
  current="$file"
  if ! perl -0pi -e "$subst" "$file" 2>/dev/null; then
    cp "$backup" "$file"; current=""
    row "$name" "bad substitution"
    continue
  fi
  if cmp -s "$file" "$backup"; then
    current=""
    row "$name" "not applied (no match)"
    continue
  fi
  printf '| %s |' "$name"
  for i in "${!commands[@]}"; do
    out="$(bash -c "${commands[$i]}" 2>&1)" && status=0 || status=$?
    if [ "$status" -eq 0 ]; then
      printf ' **missed** |'
    elif printf '%s' "$out" | grep -qE '\[build failed\]|cannot use|undefined:|declared and not used'; then
      printf ' build failed |'
    else
      printf ' caught |'
    fi
  done
  printf '\n'
  cp "$backup" "$file"; current=""
done < "$tsv"
