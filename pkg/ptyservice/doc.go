// Package ptyservice implements the server side of the RemotePTY gRPC API.
//
// It keeps the transport wiring, PTY process lifecycle, and protocol
// validation in one place while delegating daemon/session ownership and rate
// limiting to injected collaborators.
package ptyservice
