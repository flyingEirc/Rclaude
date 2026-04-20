// Package ptyclient bridges a local terminal to a server-side PTY over a
// transport-agnostic stream interface.
//
// Callers provide the transport stream plus stdin/stdout/resize collaborators.
// This package only owns the PTY client loop; dialing, raw-mode setup, and CLI
// wiring live in higher layers.
package ptyclient
