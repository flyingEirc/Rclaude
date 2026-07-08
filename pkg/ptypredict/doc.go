// Package ptypredict implements mosh-style predictive local echo for the PTY
// passthrough client (design notes: docs/reference/mosh.md).
//
// The engine sits between the local terminal and the server stream. User
// keystrokes are forwarded to the server unmodified; for a whitelisted subset
// (printable narrow runes, backspace over own predictions, cursor moves) the
// engine additionally paints the expected echo into the local terminal
// immediately, as an overlay it can undo. A shadow terminal (pkg/vtshadow)
// fed with the exact server output is the authoritative screen state; the
// server's EchoAck watermark tells the engine when a prediction is old enough
// to be judged against that state. Confirmed predictions converge silently,
// wrong ones roll the overlay back to the authoritative state (mosh's
// "reset"), and the epoch mechanism keeps predictions after uncertain input
// invisible until one of them is proven right.
package ptypredict
