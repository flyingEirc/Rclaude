package logx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/logx"
)

func TestNewJSONHandler(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	l := logx.New(logx.Options{
		Level: slog.LevelDebug,

		Format: logx.FormatJSON,

		Output: &buf,
	})

	l.Info("hello", "user", "alice", "n", 7)

	var entry map[string]any

	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))

	assert.Equal(t, "INFO", entry["level"])

	assert.Equal(t, "hello", entry["msg"])

	assert.Equal(t, "alice", entry["user"])

	assert.InDelta(t, 7, entry["n"], 0.0001)
}

func TestNewTextHandler(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	l := logx.New(logx.Options{
		Level: slog.LevelInfo,

		Format: logx.FormatText,

		Output: &buf,
	})

	l.Info("hi", "k", "v")

	out := buf.String()

	assert.Contains(t, out, "msg=hi")

	assert.Contains(t, out, "k=v")

	assert.Contains(t, out, "level=INFO")
}

func TestNewLevelFilter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	l := logx.New(logx.Options{
		Level: slog.LevelWarn,

		Format: logx.FormatJSON,

		Output: &buf,
	})

	l.Debug("debug-line")

	l.Info("info-line")

	l.Warn("warn-line")

	got := buf.String()

	assert.NotContains(t, got, "debug-line")

	assert.NotContains(t, got, "info-line")

	assert.Contains(t, got, "warn-line")
}

func TestNewDefaultsToJSONOnUnknownFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	l := logx.New(logx.Options{
		Format: "unknown",

		Output: &buf,
	})

	l.Info("hi")

	// 默认 JSON 输出可解析。

	out := strings.TrimSpace(buf.String())

	var entry map[string]any

	require.NoError(t, json.Unmarshal([]byte(out), &entry))

	assert.Equal(t, "hi", entry["msg"])
}

func TestNewEmptyOptionsUsesStderrAndJSON(t *testing.T) {
	t.Parallel()

	// 仅验证函数返回非 nil 且不 panic；输出指向 stderr 不便断言。

	l := logx.New(logx.Options{})

	require.NotNil(t, l)

	l.Info("smoke")
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	l := logx.New(logx.Options{Output: &buf})

	ctx := logx.WithContext(context.Background(), l)

	got := logx.FromContext(ctx)

	assert.Same(t, l, got)
}

func TestFromContextNilCtxFallback(t *testing.T) {
	t.Parallel()

	got := logx.FromContext(nil) //nolint:staticcheck // intentional nil ctx test

	assert.NotNil(t, got)

	assert.Same(t, slog.Default(), got)
}

func TestFromContextMissingFallback(t *testing.T) {
	t.Parallel()

	got := logx.FromContext(context.Background())

	assert.Same(t, slog.Default(), got)
}

func TestWithContextNilLoggerNoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	got := logx.WithContext(ctx, nil)

	// 不变更 ctx，FromContext 仍回退到 default。

	assert.Same(t, slog.Default(), logx.FromContext(got))
}
