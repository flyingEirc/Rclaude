package auth

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MetadataKey 是 gRPC metadata 中存放 token 的键，必须小写。
const MetadataKey = "x-rclaude-token"

// 错误集合：调用方应使用 errors.Is 比较。
var (
	// ErrMissingMetadata 表示请求 ctx 没有 incoming metadata。
	ErrMissingMetadata = errors.New("auth: missing metadata in incoming context")
	// ErrMissingToken 表示 metadata 中没有 token 字段。
	ErrMissingToken = errors.New("auth: missing token in metadata")
	// ErrInvalidToken 表示 token 不被 Verifier 接受。
	ErrInvalidToken = errors.New("auth: invalid token")
)

// Verifier 决定一个 token 是否有效，并解析出对应 user_id。
type Verifier interface {
	Verify(token string) (userID string, ok bool)
}

// staticVerifier 用静态 map 实现 Verifier。
type staticVerifier struct {
	tokens map[string]string
}

// NewStaticVerifier 用 token→userID 映射构造一个 Verifier。
// 调用方传入 m 后不应再修改它（零拷贝持有）。
func NewStaticVerifier(m map[string]string) Verifier {
	return &staticVerifier{tokens: m}
}

// Verify 实现 Verifier 接口。
func (v *staticVerifier) Verify(token string) (string, bool) {
	uid, ok := v.tokens[token]
	return uid, ok
}

// userIDCtxKey 是 context 中存放 userID 的键类型。
type userIDCtxKey struct{}

// WithUserID 把 userID 注入 ctx。一般由拦截器调用。
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDCtxKey{}, userID)
}

// UserIDFromContext 从拦截器处理过的 ctx 取出 userID。
func UserIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(userIDCtxKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// FromIncomingContext 从 incoming gRPC ctx 提取并验证 token，
// 成功返回 userID。验证失败返回 wrapped error。
func FromIncomingContext(ctx context.Context, v Verifier) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", ErrMissingMetadata
	}
	values := md.Get(MetadataKey)
	if len(values) == 0 || values[0] == "" {
		return "", ErrMissingToken
	}
	uid, ok := v.Verify(values[0])
	if !ok {
		return "", ErrInvalidToken
	}
	return uid, nil
}

// NewOutgoingContext 在 outgoing context 注入 token，供 Daemon 端调用。
func NewOutgoingContext(ctx context.Context, token string) context.Context {
	md := metadata.Pairs(MetadataKey, token)
	return metadata.NewOutgoingContext(ctx, md)
}

// UnaryServerInterceptor 返回一个验证 token 的 unary 拦截器。
// 验证成功后把 userID 注入 ctx 再调下游 handler。
func UnaryServerInterceptor(v Verifier) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		uid, err := FromIncomingContext(ctx, v)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		return handler(WithUserID(ctx, uid), req)
	}
}

// wrappedStream 用于把携带 userID 的 ctx 透出给 stream handler。
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// StreamServerInterceptor 返回一个验证 token 的 stream 拦截器。
func StreamServerInterceptor(v Verifier) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		uid, err := FromIncomingContext(ss.Context(), v)
		if err != nil {
			return status.Error(codes.Unauthenticated, err.Error())
		}
		ws := &wrappedStream{ServerStream: ss, ctx: WithUserID(ss.Context(), uid)}
		return handler(srv, ws)
	}
}
