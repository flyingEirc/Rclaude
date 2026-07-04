package logx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// Format 表示日志输出格式。
type Format string

const (
	// FormatJSON 结构化 JSON 日志，机器友好，默认格式。
	FormatJSON Format = "json"
	// FormatText console 编码，开发期人读友好。
	FormatText Format = "text"

	// DefaultFilename 是未指定文件名时使用的日志文件名。
	DefaultFilename = "rclaude.log"
	// DefaultMaxSizeMB 是单个日志文件触发轮转的默认大小（MB）。
	DefaultMaxSizeMB = 100
	// DefaultMaxBackups 是默认保留的轮转文件个数。
	DefaultMaxBackups = 3
	// DefaultMaxAgeDays 是轮转文件默认保留天数。
	DefaultMaxAgeDays = 7
)

// Logger 是应用内统一的结构化日志接口；kv 为交替出现的键值对。
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
	// With 返回附带固定字段的派生 Logger。
	With(kv ...any) Logger
}

// Options 配置 logger 工厂；所有字段均可省略。
type Options struct {
	// Level 取值 debug/info/warn/error，默认 info。
	Level string
	// Format 默认 FormatJSON。
	Format Format
	// Dir 是日志目录，默认 DefaultDir()；不存在时自动创建。
	Dir string
	// Filename 是日志文件名，默认 DefaultFilename。
	Filename string
	// MaxSizeMB / MaxBackups / MaxAgeDays 是 lumberjack 轮转参数，
	// 非正值回退到对应默认值。
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	// Output 仅供测试注入：非 nil 时日志直接写入该 writer，不落盘。
	Output io.Writer
}

// New 构造写入本地日志文件的 Logger；调用方负责在进程退出前 Close。
func New(opts Options) (*FileLogger, error) {
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	sink, closer, err := openSink(opts)
	if err != nil {
		return nil, err
	}
	zl := zap.New(zapcore.NewCore(newEncoder(opts.Format), sink, level))
	return &FileLogger{
		zapLogger: zapLogger{s: zl.Sugar()},
		zl:        zl,
		closer:    closer,
	}, nil
}

// FileLogger 是绑定输出资源的 Logger 实现。
type FileLogger struct {
	zapLogger
	zl     *zap.Logger
	closer io.Closer
}

// Close 冲刷缓冲并关闭日志文件。
func (l *FileLogger) Close() error {
	err := l.zl.Sync()
	if l.closer != nil {
		err = errors.Join(err, l.closer.Close())
	}
	if err != nil {
		return fmt.Errorf("logx: close logger: %w", err)
	}
	return nil
}

// zapLogger 用 zap.SugaredLogger 实现 Logger 接口。
type zapLogger struct {
	s *zap.SugaredLogger
}

func (l zapLogger) Debug(msg string, kv ...any) { l.s.Debugw(msg, kv...) }
func (l zapLogger) Info(msg string, kv ...any)  { l.s.Infow(msg, kv...) }
func (l zapLogger) Warn(msg string, kv ...any)  { l.s.Warnw(msg, kv...) }
func (l zapLogger) Error(msg string, kv ...any) { l.s.Errorw(msg, kv...) }

// With 返回附带固定字段的派生 Logger。
func (l zapLogger) With(kv ...any) Logger { return zapLogger{s: l.s.With(kv...)} }

// nopLogger 丢弃全部日志，用作未注入 logger 时的静默回退。
type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// With 返回自身；nop logger 不携带字段。
func (n nopLogger) With(...any) Logger { return n }

// Nop 返回丢弃一切输出的 Logger。
func Nop() Logger { return nopLogger{} }

// DefaultDir 返回默认日志目录 ~/.rclaude/logs；
// 无法确定家目录时退回系统临时目录下的 rclaude/logs。
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "rclaude", "logs")
	}
	return filepath.Join(home, ".rclaude", "logs")
}

// loggerCtxKey 是 context 中存放 logger 的键类型。
type loggerCtxKey struct{}

// WithContext 把 logger 注入 ctx；logger 为 nil 时原样返回 ctx。
func WithContext(ctx context.Context, l Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

// FromContext 从 ctx 取出 logger；未注入时回退到静默的 Nop，
// 保证任何路径都不会向终端输出日志。
func FromContext(ctx context.Context) Logger {
	if ctx == nil {
		return Nop()
	}
	if v, ok := ctx.Value(loggerCtxKey{}).(Logger); ok && v != nil {
		return v
	}
	return Nop()
}

func parseLevel(level string) (zapcore.Level, error) {
	if strings.TrimSpace(level) == "" {
		return zapcore.InfoLevel, nil
	}
	parsed, err := zapcore.ParseLevel(strings.ToLower(strings.TrimSpace(level)))
	if err != nil {
		return zapcore.InfoLevel, fmt.Errorf("logx: parse log level %q: %w", level, err)
	}
	return parsed, nil
}

func newEncoder(format Format) zapcore.Encoder {
	cfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}
	if normalizeFormat(format) == FormatText {
		return zapcore.NewConsoleEncoder(cfg)
	}
	return zapcore.NewJSONEncoder(cfg)
}

func openSink(opts Options) (zapcore.WriteSyncer, io.Closer, error) {
	if opts.Output != nil {
		return zapcore.AddSync(opts.Output), nil, nil
	}

	dir := opts.Dir
	if dir == "" {
		dir = DefaultDir()
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("logx: create log dir %q: %w", dir, err)
	}
	name := opts.Filename
	if name == "" {
		name = DefaultFilename
	}
	writer := &lumberjack.Logger{
		Filename:   filepath.Join(dir, name),
		MaxSize:    positiveOrDefault(opts.MaxSizeMB, DefaultMaxSizeMB),
		MaxBackups: positiveOrDefault(opts.MaxBackups, DefaultMaxBackups),
		MaxAge:     positiveOrDefault(opts.MaxAgeDays, DefaultMaxAgeDays),
	}
	return zapcore.AddSync(writer), writer, nil
}

func positiveOrDefault(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// normalizeFormat 把 Options.Format 规范化为 FormatJSON / FormatText。
// 空值或未识别值默认 JSON。
func normalizeFormat(f Format) Format {
	if strings.EqualFold(string(f), string(FormatText)) {
		return FormatText
	}
	return FormatJSON
}
