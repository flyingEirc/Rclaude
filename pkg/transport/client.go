package transport

import (
	"context"
	"errors"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
)

// 错误集合：调用方应使用 errors.Is 比较。
var (
	// ErrEmptyAddress 表示 DialOptions.Address 为空。
	ErrEmptyAddress = errors.New("transport: empty address")
	// ErrNilConn 表示 OpenStream 收到 nil *grpc.ClientConn。
	ErrNilConn = errors.New("transport: nil conn")
)

// DialOptions 控制 Dial 的连接行为。Phase 2 固定使用明文 gRPC。
type DialOptions struct {
	// Address 是 Server 的 gRPC 地址，例如 "1.2.3.4:9000"。
	Address string
	// Dialer 可选；非 nil 时通过 grpc.WithContextDialer 注入，
	// 用于 bufconn 集成测试或特殊网络场景。
	Dialer func(context.Context, string) (net.Conn, error)
}

// Dial 建立到 Server 的 gRPC 明文连接。
// grpc.NewClient 本身是懒连接，返回成功并不代表远端可达。
func Dial(_ context.Context, opts DialOptions) (*grpc.ClientConn, error) {
	if opts.Address == "" {
		return nil, ErrEmptyAddress
	}
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	if opts.Dialer != nil {
		dialOpts = append(dialOpts, grpc.WithContextDialer(opts.Dialer))
	}
	conn, err := grpc.NewClient(opts.Address, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("transport: dial %q: %w", opts.Address, err)
	}
	return conn, nil
}

// OpenStream 在已建立的 conn 上开 RemoteFS.Connect 双向流，
// 并把 token 注入 outgoing metadata 供 Server 端拦截器验证。
func OpenStream(
	ctx context.Context,
	conn *grpc.ClientConn,
	token string,
) (remotefsv1.RemoteFS_ConnectClient, error) {
	if conn == nil {
		return nil, ErrNilConn
	}
	outCtx := auth.NewOutgoingContext(ctx, token)
	client := remotefsv1.NewRemoteFSClient(conn)
	stream, err := client.Connect(outCtx)
	if err != nil {
		return nil, fmt.Errorf("transport: open stream: %w", err)
	}
	return stream, nil
}
