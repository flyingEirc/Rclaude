package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	// ErrCAFileEmpty 表示读取到的自定义 CA 文件不含任何证书。
	ErrCAFileEmpty = errors.New("transport: ca file contains no certificates")
)

// TLSConfig 描述客户端到 Server 的 TLS 校验行为。
// 为 nil 时 Dial 走明文 gRPC；非 nil 时用 credentials.NewTLS。
type TLSConfig struct {
	// ServerName 是 SNI 与证书校验用的主机名；为空时 gRPC 用 Address 的 host。
	// 当客户端直连源站 IP、但证书签发给某域名时，必须显式设置为该域名。
	ServerName string
	// CAFile 指向自定义根 CA（PEM）。内部 CA / 自签场景填写；
	// 公网可信证书（如 Let's Encrypt）留空以使用系统根。
	CAFile string
	// InsecureSkipVerify 跳过证书校验，仅用于诊断，切勿用于生产。
	InsecureSkipVerify bool
}

// DialOptions 控制 Dial 的连接行为。
type DialOptions struct {
	// Address 是 Server 的 gRPC 地址，例如 "1.2.3.4:9000"。
	Address string
	// Dialer 可选；非 nil 时通过 grpc.WithContextDialer 注入，
	// 用于 bufconn 集成测试或特殊网络场景。
	Dialer func(context.Context, string) (net.Conn, error)
	// TLS 可选；非 nil 时启用 TLS（Caddy 等前置终止 TLS 的场景）。
	// 为 nil 时保持明文 gRPC，向后兼容既有行为。
	TLS *TLSConfig
}

// Dial 建立到 Server 的 gRPC 连接。
// opts.TLS 为 nil 时是明文连接；非 nil 时启用 TLS。
// grpc.NewClient 本身是懒连接，返回成功并不代表远端可达。
func Dial(_ context.Context, opts DialOptions) (*grpc.ClientConn, error) {
	if opts.Address == "" {
		return nil, ErrEmptyAddress
	}
	creds, err := transportCredentials(opts.TLS)
	if err != nil {
		return nil, err
	}
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
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

// transportCredentials 依据 TLSConfig 选择明文或 TLS 凭据。
func transportCredentials(cfg *TLSConfig) (credentials.TransportCredentials, error) {
	if cfg == nil {
		return insecure.NewCredentials(), nil
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.ServerName,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // 由配置显式开启，仅诊断用
	}
	if cfg.CAFile != "" {
		pool, err := loadCertPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}
	return credentials.NewTLS(tlsCfg), nil
}

// loadCertPool 读取 PEM 格式的 CA 文件并构造仅含它的根池。
func loadCertPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile) //nolint:gosec // caFile 来自受信任的服务端配置，非外部输入
	if err != nil {
		return nil, fmt.Errorf("transport: read ca file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("transport: %q: %w", caFile, ErrCAFileEmpty)
	}
	return pool, nil
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
