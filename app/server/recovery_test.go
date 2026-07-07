package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"flyingEirc/Rclaude/pkg/logx"
)

// TestRecoveryStreamInterceptorConvertsPanic 保证 handler 同步栈上的 panic 被
// 捕获并转换为 codes.Internal，而不是冒泡崩溃进程。
func TestRecoveryStreamInterceptorConvertsPanic(t *testing.T) {
	t.Parallel()
	interceptor := recoveryStreamInterceptor(logx.Nop())

	var err error
	require.NotPanics(t, func() {
		err = interceptor(nil, nil, &grpc.StreamServerInfo{FullMethod: "/remotefs.v1.RemoteFS/Connect"},
			func(any, grpc.ServerStream) error { panic("boom") })
	})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))
}

// TestRecoveryStreamInterceptorPassesThroughError 保证正常路径下 handler 的返回
// 错误原样透传，不被 recovery 逻辑干扰。
func TestRecoveryStreamInterceptorPassesThroughError(t *testing.T) {
	t.Parallel()
	interceptor := recoveryStreamInterceptor(logx.Nop())
	sentinel := errors.New("downstream failure")

	err := interceptor(nil, nil, &grpc.StreamServerInfo{FullMethod: "/remotefs.v1.RemoteFS/Connect"},
		func(any, grpc.ServerStream) error { return sentinel })
	assert.ErrorIs(t, err, sentinel)
}

// TestRecoveryStreamInterceptorNilLogger 保证 nil logger 时回退到 Nop 而非 panic。
func TestRecoveryStreamInterceptorNilLogger(t *testing.T) {
	t.Parallel()
	interceptor := recoveryStreamInterceptor(nil)

	var err error
	require.NotPanics(t, func() {
		err = interceptor(nil, nil, &grpc.StreamServerInfo{FullMethod: "/x/Y"},
			func(any, grpc.ServerStream) error { panic("boom") })
	})
	assert.Equal(t, codes.Internal, status.Code(err))
}
