package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Format 表示日志输出格式。
type Format string

const (
	// FormatJSON 使用 slog.JSONHandler，结构化日志，机器友好。
	FormatJSON Format = "json"
	// FormatText 使用 slog.TextHandler，开发期人读友好。
	FormatText Format = "text"
)

// Options 配置 logger 工厂；所有字段均可省略。
type Options struct {
	// Level 默认 slog.LevelInfo。
	Level slog.Level
	// Format 默认 FormatJSON。
	Format Format
	// Output 默认 os.Stderr。
	Output io.Writer
	// AddSource 是否在日志中附加调用位置。
	AddSource bool
}

// New 构造一个 *slog.Logger，使用 Options 中的设置。
// 字段全空时返回与 slog.Default 等价但绑定 os.Stderr 的 JSON logger。
func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}
	handlerOpts := &slog.HandlerOptions{
		Level:     opts.Level,
		AddSource: opts.AddSource,
	}
	var handler slog.Handler
	switch normalizeFormat(opts.Format) {
	case FormatText:
		handler = slog.NewTextHandler(out, handlerOpts)
	default:
		handler = slog.NewJSONHandler(out, handlerOpts)
	}
	return slog.New(handler)
}

// loggerCtxKey 是 context 中存放 logger 的键类型。
type loggerCtxKey struct{}

// WithContext 把 logger 注入 ctx。
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

// FromContext 从 ctx 取出 logger；若未注入则回退到 slog.Default()。
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if v, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && v != nil {
		return v
	}
	return slog.Default()
}

// normalizeFormat 把 Options.Format 规范化为 FormatJSON / FormatText。
// 空值或未识别值默认 JSON。
func normalizeFormat(f Format) Format {
	switch strings.ToLower(string(f)) {
	case string(FormatText):
		return FormatText
	default:
		return FormatJSON
	}
}
