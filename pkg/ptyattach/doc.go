// Package ptyattach attaches the local interactive terminal to a remote
// agent PTY session: it loads the daemon config, puts the terminal into raw
// mode, dials the server, performs the attach handshake (declaring which
// agent program the session runs) and bridges IO until the session ends.
//
// It is used by the unified rclaude entry. Session endings are mapped to
// *ExitError so callers can translate them into process exit codes.
package ptyattach
