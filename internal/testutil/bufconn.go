package testutil

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// bufconnSize 是 bufconn 的缓冲上限；1 MiB 对 Phase 2 的测试足够。
const bufconnSize = 1 << 20

// NewBufconnServer 启动一个基于 bufconn 的 gRPC Server，
// 注册给定的 RemoteFSServer 实现，并返回一个可供 grpc.WithContextDialer 使用的 dialer。
// 清理工作通过 tb.Cleanup 自动完成，调用方无需再手动 Stop。
func NewBufconnServer(
	tb testing.TB,
	srv remotefsv1.RemoteFSServer,
) func(context.Context, string) (net.Conn, error) {
	tb.Helper()
	lis := bufconn.Listen(bufconnSize)
	grpcSrv := grpc.NewServer()
	remotefsv1.RegisterRemoteFSServer(grpcSrv, srv)

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		errCh <- grpcSrv.Serve(lis)
	}()

	tb.Cleanup(func() {
		grpcSrv.Stop()
		if err := lis.Close(); err != nil {
			tb.Errorf("testutil: close bufconn listener: %v", err)
		}
		<-done
		if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			tb.Errorf("testutil: serve bufconn listener: %v", err)
		}
	})

	return func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
}
