package safepath_test

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/safepath"
)

func TestClean(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"empty", "", "", safepath.ErrEmptyPath},
		{"root slash", "/", "", nil},
		{"dot", ".", "", nil},
		{"single", "a", "a", nil},
		{"trailing slash", "a/b/", "a/b", nil},
		{"dot prefix", "./a/b", "a/b", nil},
		{"middle dot", "a/./b", "a/b", nil},
		{"middle dotdot", "a/../b", "b", nil},
		{"chained", "a/./b/../c", "a/c", nil},
		{"only dotdot", "..", "", safepath.ErrEscape},
		{"escape with slash", "../etc/passwd", "", safepath.ErrEscape},
		{"backslash to slash", `a\b\c`, "a/b/c", nil},
		{"mixed slash", `a/b\c`, "a/b/c", nil},
		{"deep escape", "a/../../etc", "", safepath.ErrEscape},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := safepath.Clean(tc.in)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErr), "want %v, got %v", tc.wantErr, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJoin(t *testing.T) {
	t.Parallel()
	base := absBase()

	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		got, err := safepath.Join(base, "a/b/c")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "a", "b", "c"), got)
	})

	t.Run("empty rel returns base", func(t *testing.T) {
		t.Parallel()
		got, err := safepath.Join(base, ".")
		require.NoError(t, err)
		assert.Equal(t, filepath.Clean(base), got)
	})

	t.Run("escape via dotdot", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.Join(base, "../outside")
		require.ErrorIs(t, err, safepath.ErrEscape)
	})

	t.Run("rel is unix abs", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.Join(base, "/etc/passwd")
		require.ErrorIs(t, err, safepath.ErrAbsoluteRel)
	})

	t.Run("rel is windows abs", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.Join(base, `C:\Windows\notepad.exe`)
		require.ErrorIs(t, err, safepath.ErrAbsoluteRel)
	})

	t.Run("rel is backslash root", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.Join(base, `\foo`)
		require.ErrorIs(t, err, safepath.ErrAbsoluteRel)
	})

	t.Run("base not abs", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.Join("relative/base", "a")
		require.ErrorIs(t, err, safepath.ErrBaseNotAbs)
	})

	t.Run("empty base", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.Join("", "a")
		require.ErrorIs(t, err, safepath.ErrEmptyPath)
	})
}

func TestIsWithin(t *testing.T) {
	t.Parallel()
	base := absBase()

	t.Run("equal", func(t *testing.T) {
		t.Parallel()
		ok, err := safepath.IsWithin(base, base)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("child", func(t *testing.T) {
		t.Parallel()
		ok, err := safepath.IsWithin(base, filepath.Join(base, "a", "b"))
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("sibling", func(t *testing.T) {
		t.Parallel()
		sib := absSibling()
		ok, err := safepath.IsWithin(base, sib)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("string prefix but not subpath", func(t *testing.T) {
		t.Parallel()
		// /workspace/foo vs /workspace/foobar — 字符串前缀但不是父子。
		near := base + "bar"
		ok, err := safepath.IsWithin(base, near)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("base not abs", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.IsWithin("rel/base", absBase())
		require.ErrorIs(t, err, safepath.ErrBaseNotAbs)
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		_, err := safepath.IsWithin("", base)
		require.ErrorIs(t, err, safepath.ErrEmptyPath)
	})
}

func TestToFromSlash(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a/b/c", safepath.ToSlash(`a\b\c`))
	assert.Equal(t, "a/b/c", safepath.ToSlash("a/b/c"))
	got := safepath.FromSlash("a/b/c")
	assert.Equal(t, filepath.Join("a", "b", "c"), got)
}

// absBase 返回一个跨平台的绝对路径用作测试 base。
func absBase() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace\foo`
	}
	return "/workspace/foo"
}

// absSibling 返回与 absBase 同级但不同名的绝对路径。
func absSibling() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace\bar`
	}
	return "/workspace/bar"
}
