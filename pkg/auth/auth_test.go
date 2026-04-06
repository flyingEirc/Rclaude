package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"flyingEirc/Rclaude/pkg/auth"
)

func newVerifier() auth.Verifier {
	return auth.NewStaticVerifier(map[string]string{
		"tok-alice": "alice",
		"tok-bob":   "bob",
	})
}

func TestStaticVerifierHit(t *testing.T) {
	t.Parallel()
	v := newVerifier()
	uid, ok := v.Verify("tok-alice")
	assert.True(t, ok)
	assert.Equal(t, "alice", uid)
}

func TestStaticVerifierMiss(t *testing.T) {
	t.Parallel()
	v := newVerifier()
	uid, ok := v.Verify("nope")
	assert.False(t, ok)
	assert.Equal(t, "", uid)
}

func TestFromIncomingContextOK(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(auth.MetadataKey, "tok-bob"),
	)
	uid, err := auth.FromIncomingContext(ctx, newVerifier())
	require.NoError(t, err)
	assert.Equal(t, "bob", uid)
}

func TestFromIncomingContextMissingMD(t *testing.T) {
	t.Parallel()
	_, err := auth.FromIncomingContext(context.Background(), newVerifier())
	require.Error(t, err)
	assert.True(t, errors.Is(err, auth.ErrMissingMetadata))
}

func TestFromIncomingContextMissingToken(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("other-key", "x"),
	)
	_, err := auth.FromIncomingContext(ctx, newVerifier())
	require.Error(t, err)
	assert.True(t, errors.Is(err, auth.ErrMissingToken))
}

func TestFromIncomingContextInvalidToken(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(auth.MetadataKey, "wrong"),
	)
	_, err := auth.FromIncomingContext(ctx, newVerifier())
	require.Error(t, err)
	assert.True(t, errors.Is(err, auth.ErrInvalidToken))
}

func TestUnaryInterceptorSuccess(t *testing.T) {
	t.Parallel()
	interceptor := auth.UnaryServerInterceptor(newVerifier())
	called := false
	handler := func(ctx context.Context, _ any) (any, error) {
		called = true
		uid, ok := auth.UserIDFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, "alice", uid)
		return "ok", nil
	}
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(auth.MetadataKey, "tok-alice"),
	)
	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

func TestUnaryInterceptorReject(t *testing.T) {
	t.Parallel()
	interceptor := auth.UnaryServerInterceptor(newVerifier())
	handler := func(_ context.Context, _ any) (any, error) {
		t.Fatal("handler should not be called")
		return nil, nil //nolint:nilnil // unreachable
	}
	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	require.Error(t, err)
	assert.Nil(t, resp)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

// fakeStream 用于在测试中包装 ctx 而无需真实 gRPC server。
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestStreamInterceptorSuccess(t *testing.T) {
	t.Parallel()
	interceptor := auth.StreamServerInterceptor(newVerifier())
	called := false
	handler := func(_ any, ss grpc.ServerStream) error {
		called = true
		uid, ok := auth.UserIDFromContext(ss.Context())
		assert.True(t, ok)
		assert.Equal(t, "bob", uid)
		return nil
	}
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs(auth.MetadataKey, "tok-bob"),
	)
	err := interceptor(nil, &fakeStream{ctx: ctx}, &grpc.StreamServerInfo{}, handler)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestStreamInterceptorReject(t *testing.T) {
	t.Parallel()
	interceptor := auth.StreamServerInterceptor(newVerifier())
	handler := func(_ any, _ grpc.ServerStream) error {
		t.Fatal("handler should not be called")
		return nil
	}
	err := interceptor(nil, &fakeStream{ctx: context.Background()}, &grpc.StreamServerInfo{}, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestNewOutgoingContextRoundTrip(t *testing.T) {
	t.Parallel()
	out := auth.NewOutgoingContext(context.Background(), "tok-alice")
	md, ok := metadata.FromOutgoingContext(out)
	require.True(t, ok)
	values := md.Get(auth.MetadataKey)
	require.Len(t, values, 1)
	assert.Equal(t, "tok-alice", values[0])
}

func TestUserIDFromContextEmpty(t *testing.T) {
	t.Parallel()
	_, ok := auth.UserIDFromContext(context.Background())
	assert.False(t, ok)

	_, ok = auth.UserIDFromContext(nil) //nolint:staticcheck // intentional nil ctx test
	assert.False(t, ok)
}
