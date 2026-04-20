#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <user_id> <expected_file>" >&2
  exit 2
}

wait_for_file() {
  path=$1
  attempts=20

  while [ "$attempts" -gt 0 ]; do
    if [ -f "$path" ]; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 0.2
  done

  echo "path did not become visible in time: $path" >&2
  return 1
}

if [ "$#" -ne 2 ]; then
  usage
fi

mountpoint=${RCLAUDE_MOUNTPOINT:-/workspace}
user_id=$1
expected_file=$2
user_root="${mountpoint%/}/$user_id"
expected_path="$user_root/$expected_file"
stamp=$(date -u +%Y%m%d%H%M%S)-$$
file_path="$user_root/.rclaude-smoke-$stamp.txt"
renamed_path="$user_root/.rclaude-smoke-$stamp.moved.txt"

cleanup() {
  rm -f "$file_path" "$renamed_path" 2>/dev/null || true
}

trap cleanup EXIT INT HUP TERM

if [ ! -d "$mountpoint" ]; then
  echo "mountpoint does not exist: $mountpoint" >&2
  exit 1
fi

if [ ! -d "$user_root" ]; then
  echo "user root does not exist: $user_root" >&2
  exit 1
fi

if [ ! -f "$expected_path" ]; then
  echo "expected file does not exist: $expected_path" >&2
  exit 1
fi

echo "==> ls $user_root"
ls -la "$user_root"

echo "==> cat $expected_path"
cat "$expected_path"

echo "==> write $file_path"
printf 'phase7 smoke %s\n' "$stamp" > "$file_path"
test -f "$file_path"

echo "==> mv $file_path $renamed_path"
mv "$file_path" "$renamed_path"
# FUSE directory views may lag for a moment after rename, so wait briefly for the new path.
wait_for_file "$renamed_path"

echo "==> rm $renamed_path"
rm "$renamed_path"

echo "remote smoke passed for $user_id"
