#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
usage: tools/pty-smoke.sh <daemon-config-path>

Modes:
  default / RCLAUDE_PTY_MODE=manual
      Run rclaude-claude directly for a human check of the real claude entry.
  RCLAUDE_PTY_MODE=scripted
      Drive one repeatable shell smoke via script(1).
      This mode expects server-side pty.binary to point at a shell such as /bin/sh.

Optional environment:
  RCLAUDE_PTY_BIN          Override the local rclaude-claude binary path.
  RCLAUDE_PTY_TRANSCRIPT   Transcript path for scripted mode.
  RCLAUDE_PTY_INPUT_FILE   Input file for scripted mode.
  RCLAUDE_PTY_EXPECT       Expected marker in transcript. Default: __RCLAUDE_PTY_SMOKE__.
  RCLAUDE_PTY_EXPECT_EXIT  Expected exit code in scripted mode. Default: 7.
  RCLAUDE_PTY_EXPECT_CWD   Optional substring that must appear in the transcript.
EOF
  exit 2
}

find_pty_bin() {
  if [ -n "${RCLAUDE_PTY_BIN:-}" ]; then
    printf '%s\n' "$RCLAUDE_PTY_BIN"
    return 0
  fi

  if [ -x "./bin/rclaude-claude" ]; then
    printf '%s\n' "./bin/rclaude-claude"
    return 0
  fi

  if command -v rclaude-claude >/dev/null 2>&1; then
    command -v rclaude-claude
    return 0
  fi

  return 1
}

strip_quotes() {
  printf '%s' "$1" | tr -d '"'
}

make_launcher() {
  launcher_path=$1

  cat > "$launcher_path" <<EOF
#!/bin/sh
exec "$pty_bin" --config "$config_path"
EOF
  chmod +x "$launcher_path"
}

build_default_input() {
  input_path=$1
  cat > "$input_path" <<'EOF'
printf '__RCLAUDE_PTY_SMOKE__\n'
stty size
pwd
exit 7
EOF
}

if [ "$#" -ne 1 ]; then
  usage
fi

config_path=$1
mode=${RCLAUDE_PTY_MODE:-manual}

if [ ! -f "$config_path" ]; then
  echo "daemon config does not exist: $config_path" >&2
  exit 1
fi

if ! pty_bin=$(find_pty_bin); then
  echo "rclaude-claude binary not found; build ./app/clientpty or set RCLAUDE_PTY_BIN" >&2
  exit 1
fi

server_address=$(sed -n 's/^[[:space:]]*address:[[:space:]]*//p' "$config_path" | head -n 1)
workspace_path=$(sed -n 's/^[[:space:]]*path:[[:space:]]*//p' "$config_path" | head -n 1)

echo "==> PTY smoke"
echo "mode: $mode"
echo "binary: $pty_bin"
echo "config: $config_path"

if [ -n "$server_address" ]; then
  echo "server.address: $(strip_quotes "$server_address")"
fi

if [ -n "$workspace_path" ]; then
  echo "workspace.path: $(strip_quotes "$workspace_path")"
fi

case "$mode" in
  manual)
    cat <<'EOF'
manual checklist:
  1. Attach should land in a remote PTY session.
  2. For the real claude entry, confirm cwd is under /workspace/<user_id>.
  3. Resize the local terminal and confirm the remote layout follows.
  4. Ctrl+C or normal exit should return control to the local terminal.
EOF
    if "$pty_bin" --config "$config_path"; then
      status=0
    else
      status=$?
    fi
    echo "manual PTY run exited with status $status"
    exit "$status"
    ;;
  scripted)
    if ! command -v script >/dev/null 2>&1; then
      echo "scripted mode requires script(1) on PATH" >&2
      exit 1
    fi

    launcher=$(mktemp "${TMPDIR:-/tmp}/rclaude-pty-launch.XXXXXX")
    transcript=${RCLAUDE_PTY_TRANSCRIPT:-$(mktemp "${TMPDIR:-/tmp}/rclaude-pty-transcript.XXXXXX")}
    input_file=${RCLAUDE_PTY_INPUT_FILE:-}
    expected=${RCLAUDE_PTY_EXPECT:-__RCLAUDE_PTY_SMOKE__}
    expected_exit=${RCLAUDE_PTY_EXPECT_EXIT:-7}
    generated_input=0

    cleanup() {
      rm -f "$launcher" 2>/dev/null || true
      if [ "$generated_input" -eq 1 ]; then
        rm -f "$input_file" 2>/dev/null || true
      fi
    }
    trap cleanup EXIT INT HUP TERM

    make_launcher "$launcher"

    if [ -z "$input_file" ]; then
      input_file=$(mktemp "${TMPDIR:-/tmp}/rclaude-pty-input.XXXXXX")
      generated_input=1
      build_default_input "$input_file"
    fi

    echo "transcript: $transcript"
    echo "input: $input_file"

    if script -qefc "$launcher" "$transcript" < "$input_file"; then
      status=0
    else
      status=$?
    fi

    if [ "$status" -ne "$expected_exit" ]; then
      echo "unexpected PTY exit code: got $status want $expected_exit" >&2
      exit 1
    fi

    if ! grep -F "$expected" "$transcript" >/dev/null 2>&1; then
      echo "expected marker not found in transcript: $expected" >&2
      exit 1
    fi

    if [ -n "${RCLAUDE_PTY_EXPECT_CWD:-}" ] &&
      ! grep -F "$RCLAUDE_PTY_EXPECT_CWD" "$transcript" >/dev/null 2>&1; then
      echo "expected cwd marker not found in transcript: $RCLAUDE_PTY_EXPECT_CWD" >&2
      exit 1
    fi

    echo "scripted PTY smoke passed"
    ;;
  *)
    echo "unknown RCLAUDE_PTY_MODE: $mode" >&2
    usage
    ;;
esac
