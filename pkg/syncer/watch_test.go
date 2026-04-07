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

const watchTimeout = 3 * time.Second

func startWatch(t *testing.T, opts WatchOptions) (chan *remotefsv1.FileChange, context.CancelFunc, func() error) {
	t.Helper()

	ch := make(chan *remotefsv1.FileChange, 64)
	opts.Events = ch
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, opts)
	}()
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
			t.Fatalf("timeout waiting for matching file change")
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

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.log"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("y"), 0o600))

	ev := waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "a.txt"
	})
	assert.NotNil(t, ev)

	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case c := <-ch:
			assert.NotEqual(t, "a.log", c.GetFile().GetPath())
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
	waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "sub" &&
			c.GetType() == remotefsv1.ChangeType_CHANGE_TYPE_CREATE
	})

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
		t.Fatal("watch did not return after context cancel")
	}
}

func TestWatch_SuppressesSelfWrite(t *testing.T) {
	root := t.TempDir()
	filter := newSelfWriteFilter(2 * time.Second)
	ch, cancel, wait := startWatch(t, WatchOptions{
		Root:       root,
		SelfWrites: filter,
	})
	defer func() {
		cancel()
		assert.NoError(t, wait())
	}()

	filter.Remember("x.txt")
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0o600))

	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case ev := <-ch:
			assert.NotEqual(t, "x.txt", ev.GetFile().GetPath())
		case <-timeout:
			break loop
		}
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, "y.txt"), []byte("y"), 0o600))
	ev := waitForMatch(t, ch, func(c *remotefsv1.FileChange) bool {
		return c.GetFile().GetPath() == "y.txt"
	})
	assert.NotNil(t, ev)
}
