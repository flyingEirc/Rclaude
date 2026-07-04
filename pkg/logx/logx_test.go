package logx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/logx"
)

func newBufLogger(t *testing.T, opts logx.Options) (*logx.FileLogger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	opts.Output = buf
	l, err := logx.New(opts)
	require.NoError(t, err)
	return l, buf
}

func TestNewJSONFormat(t *testing.T) {
	t.Parallel()
	l, buf := newBufLogger(t, logx.Options{Level: "debug", Format: logx.FormatJSON})

	l.Info("hello", "user", "alice", "n", 7)
	require.NoError(t, l.Close())

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "info", entry["level"])
	assert.Equal(t, "hello", entry["msg"])
	assert.Equal(t, "alice", entry["user"])
	assert.InDelta(t, 7, entry["n"], 0.0001)
	assert.NotEmpty(t, entry["ts"])
}

func TestNewTextFormat(t *testing.T) {
	t.Parallel()
	l, buf := newBufLogger(t, logx.Options{Format: logx.FormatText})

	l.Info("hi", "k", "v")
	require.NoError(t, l.Close())

	out := buf.String()
	assert.Contains(t, out, "hi")
	assert.Contains(t, out, "info")
	assert.Contains(t, out, `"k": "v"`)
}

func TestNewLevelFilter(t *testing.T) {
	t.Parallel()
	l, buf := newBufLogger(t, logx.Options{Level: "warn"})

	l.Debug("debug-line")
	l.Info("info-line")
	l.Warn("warn-line")
	l.Error("error-line")
	require.NoError(t, l.Close())

	got := buf.String()
	assert.NotContains(t, got, "debug-line")
	assert.NotContains(t, got, "info-line")
	assert.Contains(t, got, "warn-line")
	assert.Contains(t, got, "error-line")
}

func TestNewInvalidLevel(t *testing.T) {
	t.Parallel()
	_, err := logx.New(logx.Options{Level: "loud", Output: &bytes.Buffer{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loud")
}

func TestNewDefaultsToJSONOnUnknownFormat(t *testing.T) {
	t.Parallel()
	l, buf := newBufLogger(t, logx.Options{Format: "unknown"})

	l.Info("hi")
	require.NoError(t, l.Close())

	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry))
	assert.Equal(t, "hi", entry["msg"])
}

func TestWithAddsFields(t *testing.T) {
	t.Parallel()
	l, buf := newBufLogger(t, logx.Options{})

	l.With("component", "syncer").Info("started")
	require.NoError(t, l.Close())

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "syncer", entry["component"])
}

func TestNewWritesJSONFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "logs")

	l, err := logx.New(logx.Options{
		Dir:      dir,
		Filename: "test.log",
	})
	require.NoError(t, err)
	l.Info("to-file", "k", "v")
	require.NoError(t, l.Close())

	data, err := os.ReadFile(filepath.Join(dir, "test.log")) //nolint:gosec // test path
	require.NoError(t, err)
	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry))
	assert.Equal(t, "to-file", entry["msg"])
	assert.Equal(t, "v", entry["k"])
}

func TestDefaultDirNonEmpty(t *testing.T) {
	t.Parallel()
	dir := logx.DefaultDir()
	assert.NotEmpty(t, dir)
	assert.True(t, filepath.IsAbs(dir))
}

func TestNopSilent(t *testing.T) {
	t.Parallel()
	l := logx.Nop()
	// 不 panic 即可；Nop 丢弃一切输出。
	l.Debug("d")
	l.Info("i", "k", "v")
	l.Warn("w")
	l.Error("e")
	l.With("k", "v").Info("chained")
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()
	l, _ := newBufLogger(t, logx.Options{})

	ctx := logx.WithContext(context.Background(), l)
	got := logx.FromContext(ctx)
	assert.Same(t, l, got)
}

func TestFromContextNilCtxFallsBackToNop(t *testing.T) {
	t.Parallel()
	got := logx.FromContext(nil) //nolint:staticcheck // intentional nil ctx test
	require.NotNil(t, got)
	assert.Equal(t, logx.Nop(), got)
}

func TestFromContextMissingFallsBackToNop(t *testing.T) {
	t.Parallel()
	got := logx.FromContext(context.Background())
	assert.Equal(t, logx.Nop(), got)
}

func TestWithContextNilLoggerNoop(t *testing.T) {
	t.Parallel()
	got := logx.WithContext(context.Background(), nil)
	assert.Equal(t, logx.Nop(), logx.FromContext(got))
}
