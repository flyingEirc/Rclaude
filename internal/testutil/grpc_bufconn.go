package testutil

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
)

// GRPCBufconnOptions describes which authenticated gRPC services should be
// exposed through an in-memory bufconn listener.
type GRPCBufconnOptions struct {
	Verifier  auth.Verifier
	RemoteFS  remotefsv1.RemoteFSServer
	RemotePTY remotefsv1.RemotePTYServer
}

// GRPCBufconnServer owns a bufconn-backed gRPC server for integration tests.
type GRPCBufconnServer struct {
	listener *bufconn.Listener
}

// NewGRPCBufconnServer starts an authenticated bufconn-backed gRPC server.
func NewGRPCBufconnServer(tb testing.TB, opts GRPCBufconnOptions) *GRPCBufconnServer {
	tb.Helper()

	verifier := opts.Verifier
	if verifier == nil {
		verifier = auth.NewStaticVerifier(map[string]string{})
	}

	listener := bufconn.Listen(bufconnSize)
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(verifier)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(verifier)),
	)
	if opts.RemoteFS != nil {
		remotefsv1.RegisterRemoteFSServer(grpcServer, opts.RemoteFS)
	}
	if opts.RemotePTY != nil {
		remotefsv1.RegisterRemotePTYServer(grpcServer, opts.RemotePTY)
	}

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		errCh <- grpcServer.Serve(listener)
	}()

	tb.Cleanup(func() {
		grpcServer.Stop()
		if err := listener.Close(); err != nil {
			tb.Errorf("testutil: close grpc bufconn listener: %v", err)
		}
		<-done
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			tb.Errorf("testutil: serve grpc bufconn listener: %v", err)
		}
	})

	return &GRPCBufconnServer{listener: listener}
}

// DialContext exposes the underlying bufconn dialer.
func (s *GRPCBufconnServer) DialContext(ctx context.Context, _ string) (net.Conn, error) {
	if s == nil || s.listener == nil {
		return nil, net.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.listener.DialContext(ctx)
}

// NewClientConn constructs a grpc.ClientConn wired to this bufconn server.
func (s *GRPCBufconnServer) NewClientConn() (*grpc.ClientConn, error) {
	if s == nil {
		return nil, net.ErrClosed
	}
	return grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(s.DialContext),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}
