// Package ptyhost manages PTY-bound child processes on the server side.
//
// It is intentionally transport-agnostic: callers feed it stdin via an
// io.Writer, drain stdout via an io.Reader, push window-size updates, and
// wait for the child to exit. The gRPC layer is a separate concern wired up
// in app/server. See docs/superpowers/specs/2026-04-19-remote-claudecode-pty-design.md.
package ptyhost
