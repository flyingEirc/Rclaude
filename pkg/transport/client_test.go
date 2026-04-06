package transport

import (
	"context"
	"errors"
	"net"
	"testing"

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
