package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// watchTimeout 是单条事件等待上限；fsnotify 在 macOS/Windows 上偶尔较慢。
const watchTimeout = 3 * time.Second

// startWatch 在后台 goroutine 里启动 Watch，并返回事件 channel、停止函数和等待 Watch 返回的 wait 函数。
func startWatch(t *testing.T, opts WatchOptions) (chan *remotefsv1.FileChange, context.CancelFunc, func() error) {
	t.Helper()
	ch := make(chan *remotefsv1.FileChange, 64)
	opts.Events = ch
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, opts)
	}()
	// 给 fsnotify 一个很短的初始化窗口，避免首次事件丢失。
	time.Sleep(50 * time.Millisecond)
	return ch, cancel, func() error {
		select {
		case err := <-errCh:
			return err
		case <-time.After(watchTimeout):
			return nil
		}
	}
}

// waitForMatch 在 timeout 内等待 ch 输出一个满足 predicate 的事件。
func waitForMatch(
	t *testing.T,
	ch <-chan *remotefsv1.FileChange,
	predicate func(*remotefsv1.FileChange) bool,
) *remotefsv1.FileChange {
	t.Helper()
	deadline := time.After(watchTimeout)
	for {
		select {
		case ev := <-ch:
			if predicate(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("timeout waiting for matching FileChange")
			return nil
		}
	}
}

func TestWatch_NilEvents(t *testing.T) {
	err := Watch(context.Background(), WatchOptions{Root: t.TempDir()})
	assert.ErrorIs(t, err, ErrNilEvents)
}

func TestWatch_NonAbsRoot(t *testing.T) {
	ch := make(chan *remotefsv1.FileChange, 1)
	err := Watch(context.Background(), WatchOptions{
		Root:   "relative",
		Events: ch,
	})
	assert.ErrorIs(t, err, ErrRootNotAbsolute)
}

func TestWatch_CreateFile(t *testing.T) {
	root := t.TempDir()
	ch, cancel, wait := startWatch(t, WatchOptions{Root: root})
	defer func() {
		cancel()
		assert.NoError(t, wait())
	}()

	target := filepath.Join(root, "new.txt")
	require.NoError(t, os.WriteFile(target, []byte("hi"), 0o600))

	ev := waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "new.txt" &&
			(c.GetType() == remotefsv1.ChangeType_CHANGE_TYPE_CREATE ||
				c.GetType() == remotefsv1.ChangeType_CHANGE_TYPE_MODIFY)
	})
	assert.NotNil(t, ev)
}

func TestWatch_DeleteFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "gone.txt")
	require.NoError(t, os.WriteFile(target, []byte("bye"), 0o600))

	ch, cancel, wait := startWatch(t, WatchOptions{Root: root})
	defer func() {
		cancel()
		assert.NoError(t, wait())
	}()

	require.NoError(t, os.Remove(target))

	ev := waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "gone.txt" &&
			c.GetType() == remotefsv1.ChangeType_CHANGE_TYPE_DELETE
	})
	assert.NotNil(t, ev)
}

func TestWatch_ExcludedFile(t *testing.T) {
	root := t.TempDir()
	ch, cancel, wait := startWatch(t, WatchOptions{
		Root:     root,
		Excludes: []string{"*.log"},
	})
	defer func() {
		cancel()
		assert.NoError(t, wait())
	}()

	// 触发一次排除文件的写入与一次普通文件的写入。
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.log"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("y"), 0o600))

	ev := waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "a.txt"
	})
	assert.NotNil(t, ev)

	// 再排 drain 500ms，确认没有任何 a.log 事件。
	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case c := <-ch:
			assert.NotEqual(t, "a.log", c.GetFile().GetPath(),
				"excluded file should not produce events")
		case <-timeout:
			break loop
		}
	}
}

func TestWatch_NewSubdir(t *testing.T) {
	root := t.TempDir()
	ch, cancel, wait := startWatch(t, WatchOptions{Root: root})
	defer func() {
		cancel()
		assert.NoError(t, wait())
	}()

	subdir := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(subdir, 0o750))
	// 等待 CREATE 子目录事件被处理并 Add 到 watcher。
	waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "sub" &&
			c.GetType() == remotefsv1.ChangeType_CHANGE_TYPE_CREATE
	})

	// 再在子目录下建文件，验证新目录确实被监听。
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "x.txt"), []byte("z"), 0o600))
	ev := waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "sub/x.txt"
	})
	assert.NotNil(t, ev)
}

func TestWatch_ContextCancel(t *testing.T) {
	root := t.TempDir()
	ch := make(chan *remotefsv1.FileChange, 1)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, WatchOptions{Root: root, Events: ch})
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(watchTimeout):
		t.Fatal("Watch did not return after context cancel")
	}
}
