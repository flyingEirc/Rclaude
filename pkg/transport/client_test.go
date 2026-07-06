package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestDial_EmptyAddress(t *testing.T) {
	_, err := Dial(context.Background(), DialOptions{})
	assert.ErrorIs(t, err, ErrEmptyAddress)
}

func TestDial_ReturnsLazyConn(t *testing.T) {
	conn, err := Dial(context.Background(), DialOptions{Address: "localhost:65535"})
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NoError(t, conn.Close())
}

func TestDial_WithCustomDialer(t *testing.T) {
	lis := bufconn.Listen(1 << 16)
	defer func() {
		assert.NoError(t, lis.Close())
	}()

	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := Dial(context.Background(), DialOptions{
		Address: "passthrough:///bufnet",
		Dialer:  dialer,
	})
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.NoError(t, conn.Close())
}

func TestOpenStream_NilConn(t *testing.T) {
	_, err := OpenStream(context.Background(), nil, "token")
	assert.ErrorIs(t, err, ErrNilConn)
}

func TestDial_TLSWithSystemRoots(t *testing.T) {
	// TLS 非 nil 且 CAFile 为空：应使用系统根构造凭据，Dial 懒连接成功。
	conn, err := Dial(context.Background(), DialOptions{
		Address: "rclaude.example.com:443",
		TLS:     &TLSConfig{ServerName: "rclaude.example.com"},
	})
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.NoError(t, conn.Close())
}

func TestDial_TLSWithCustomCA(t *testing.T) {
	caPath := writeTestCA(t)
	conn, err := Dial(context.Background(), DialOptions{
		Address: "127.0.0.1:443",
		TLS:     &TLSConfig{ServerName: "rclaude.example.com", CAFile: caPath},
	})
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.NoError(t, conn.Close())
}

func TestDial_TLSMissingCAFile(t *testing.T) {
	_, err := Dial(context.Background(), DialOptions{
		Address: "127.0.0.1:443",
		TLS:     &TLSConfig{CAFile: filepath.Join(t.TempDir(), "nope.pem")},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestDial_TLSEmptyCAFile(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.pem")
	require.NoError(t, os.WriteFile(empty, []byte("not a certificate"), 0o600))
	_, err := Dial(context.Background(), DialOptions{
		Address: "127.0.0.1:443",
		TLS:     &TLSConfig{CAFile: empty},
	})
	assert.ErrorIs(t, err, ErrCAFileEmpty)
}

// writeTestCA 生成一张自签 CA 证书并写入临时 PEM 文件，返回其路径。
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rclaude-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.pem")
	f, err := os.Create(path) //nolint:gosec // path 由 t.TempDir() 派生，测试专用
	require.NoError(t, err)
	defer func() { assert.NoError(t, f.Close()) }()
	require.NoError(t, pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return path
}

func TestOpenStream_AgainstBufconn(t *testing.T) {
	lis := bufconn.Listen(1 << 16)
	srv := grpc.NewServer()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(lis)
	}()
	defer func() {
		srv.Stop()
		assert.NoError(t, lis.Close())
		err := <-errCh
		assert.True(t, err == nil || errors.Is(err, grpc.ErrServerStopped))
	}()

	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := Dial(context.Background(), DialOptions{
		Address: "passthrough:///bufnet",
		Dialer:  dialer,
	})
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, conn.Close())
	}()

	stream, err := OpenStream(t.Context(), conn, "test-token")
	require.NoError(t, err)
	require.NotNil(t, stream)
}
