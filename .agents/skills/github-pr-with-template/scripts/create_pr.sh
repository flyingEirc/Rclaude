#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <title> <base-branch> [body-file]" >&2
  exit 2
fi

title="$1"
base_branch="$2"
body_file="${3:-/tmp/pr-message.md}"
current_branch="$(git branch --show-current)"

if [[ -z "$current_branch" ]]; then
  echo "failed: unable to determine current branch" >&2
  exit 1
fi

if [[ "$current_branch" == "master" ]]; then
  echo "failed: refuse to create a PR from master" >&2
  exit 1
fi

if [[ ! -f "$body_file" ]]; then
  echo "failed: PR body file not found: $body_file" >&2
  exit 1
fi

cleanup() {
  rm -f "$body_file"
}

if output="$(gh pr create --base "$base_branch" --head "$current_branch" --title "$title" --body-file "$body_file" 2>&1)"; then
  cleanup
  printf '%s\n' "$output"
  exit 0
fi

printf 'gh pr create failed.\n' >&2
printf '%s\n' "$output" >&2
printf '\nGenerated PR body (%s):\n' "$body_file" >&2
cat "$body_file" >&2
cleanup
exit 1
