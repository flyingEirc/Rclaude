package main

import (
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"flyingEirc/Rclaude/pkg/logx"
)

// recoveryStreamInterceptor 是最外层 stream 拦截器：捕获下游（auth 拦截器与业务
// handler）同步栈上的 panic，记录方法名与堆栈后转换为 codes.Internal，使单条流
// 失败，而不是让未 recover 的 panic 冒泡崩掉整个 server 进程、拖垮所有会话。
// 注意：handler 内部另起的 goroutine panic 不在此拦截范围内（Go 语义所限），
// 各自的 goroutine 需自行 recover；本拦截器只覆盖 handler 的同步调用栈。
func recoveryStreamInterceptor(logger logx.Logger) grpc.StreamServerInterceptor {
	if logger == nil {
		logger = logx.Nop()
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("grpc stream handler panic recovered",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}
