// Package ptyattach attaches the local interactive terminal to a remote
// claude PTY session: it loads the daemon config, puts the terminal into raw
// mode, dials the server, performs the attach handshake and bridges IO until
// the session ends.
//
// It is shared by the standalone clientpty binary and the unified rclaude
// entry. Session endings are mapped to *ExitError so callers can translate
// them into process exit codes.
package ptyattach
