// Package vtshadow tracks terminal screen state from an output byte stream
// without rendering anything. The predictive-echo engine feeds it the exact
// bytes forwarded to the real terminal and uses the resulting grid, cursor,
// and mode state as the authoritative baseline to place, validate, and undo
// locally predicted echo (mosh-style overlay reconciliation).
//
// The parser follows the classic VT500 state machine shape; sequences that a
// shadow cannot or need not honor are consumed and either recognized as
// no-ops or counted in Feedback.Unknown so callers can lower their trust in
// the shadow for a moment.
package vtshadow
