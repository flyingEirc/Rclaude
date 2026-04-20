package testutil

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
)

type BufconnServices struct {
	Verifier  auth.Verifier
	RemoteFS  remotefsv1.RemoteFSServer
	RemotePTY remotefsv1.RemotePTYServer
}

// NewBufconnServerWithServices starts a bufconn gRPC server and registers the
// provided RemoteFS/RemotePTY services behind the standard auth interceptor.
func NewBufconnServerWithServices(
	tb testing.TB,
	services BufconnServices,
) func(context.Context, string) (net.Conn, error) {
	tb.Helper()

	lis := bufconn.Listen(bufconnSize)
	grpcSrv := grpc.NewServer(
		grpc.StreamInterceptor(auth.StreamServerInterceptor(services.Verifier)),
	)
	if services.RemoteFS != nil {
		remotefsv1.RegisterRemoteFSServer(grpcSrv, services.RemoteFS)
	}
	if services.RemotePTY != nil {
		remotefsv1.RegisterRemotePTYServer(grpcSrv, services.RemotePTY)
	}

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		errCh <- grpcSrv.Serve(lis)
	}()

	tb.Cleanup(func() {
		grpcSrv.Stop()
		if err := lis.Close(); err != nil {
			tb.Errorf("testutil: close pty bufconn listener: %v", err)
		}
		<-done
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			tb.Errorf("testutil: serve pty bufconn listener: %v", err)
		}
	})

	return func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
}
