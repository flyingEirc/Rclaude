#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <mountpoint> <user_id>" >&2
  exit 2
fi

mountpoint="$1"
user_id="$2"
user_root="${mountpoint%/}/${user_id}"
base=".rclaude-phase5-smoke-$$"
file_path="${user_root}/${base}.txt"
renamed_path="${user_root}/${base}-renamed.txt"

cleanup() {
  rm -f "$file_path" "$renamed_path" 2>/dev/null || true
}
trap cleanup EXIT

if [[ ! -d "$mountpoint" ]]; then
  echo "mountpoint does not exist: $mountpoint" >&2
  exit 1
fi

if [[ ! -d "$user_root" ]]; then
  echo "user root does not exist under mountpoint: $user_root" >&2
  exit 1
fi

echo "==> ls mount root"
ls "$mountpoint"

echo "==> ls user root"
ls "$user_root"

echo "==> write file"
printf 'phase5 smoke\n' > "$file_path"

echo "==> cat file"
cat "$file_path"

echo "==> mv file"
mv "$file_path" "$renamed_path"
test ! -e "$file_path"

echo "==> rm file"
rm "$renamed_path"
test ! -e "$renamed_path"

echo "fuse smoke passed"
