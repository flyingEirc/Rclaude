//go:build unix

package ptyhost_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/ptyhost"
)

func TestSpawn_EchoAndExit(t *testing.T) {
	t.Parallel()

	h, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/bin/sh",
		Cwd:      t.TempDir(),
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, copyErr := io.Copy(&buf, h.Stdout()); copyErr != nil {
			t.Logf("copy PTY stdout: %v", copyErr)
		}
	}()

	_, err = h.Stdin().Write([]byte("echo hello-pty\n"))
	require.NoError(t, err)
	_, err = h.Stdin().Write([]byte("exit 0\n"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := h.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(0), info.Code)

	wg.Wait()
	assert.Contains(t, buf.String(), "hello-pty")
}

func TestSpawn_BinaryNotFound(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/no/such/binary/here",
		Cwd:      t.TempDir(),
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.Error(t, err)
}

func TestSpawn_CwdMustExist(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/bin/sh",
		Cwd:      "/definitely/no/such/dir",
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.Error(t, err)
}

func TestResize_PropagatesToChild(t *testing.T) {
	t.Parallel()

	h, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:   "/bin/sh",
		Cwd:      t.TempDir(),
		Env:      []string{"PATH=/usr/bin:/bin"},
		InitSize: ptyhost.WindowSize{Cols: 80, Rows: 24},
	})
	require.NoError(t, err)

	require.NoError(t, h.Resize(ptyhost.WindowSize{Cols: 132, Rows: 50}))

	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, copyErr := io.Copy(&buf, h.Stdout()); copyErr != nil {
			t.Logf("copy PTY stdout: %v", copyErr)
		}
	}()

	_, err = h.Stdin().Write([]byte("stty size; exit 0\n"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = h.Wait(ctx)
	require.NoError(t, err)
	wg.Wait()

	assert.True(t, strings.Contains(buf.String(), "50 132"), "expected `stty size` to report 50 132, got: %q", buf.String())
}

func TestShutdown_GracefulThenKill(t *testing.T) {
	t.Parallel()

	h, err := ptyhost.Spawn(ptyhost.SpawnReq{
		Binary:          "/bin/sh",
		Cwd:             t.TempDir(),
		Env:             []string{"PATH=/usr/bin:/bin"},
		InitSize:        ptyhost.WindowSize{Cols: 80, Rows: 24},
		GracefulTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	_, err = h.Stdin().Write([]byte("trap '' HUP; sleep 30\n"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	go func() {
		if _, copyErr := io.Copy(io.Discard, h.Stdout()); copyErr != nil {
			t.Logf("discard PTY stdout: %v", copyErr)
		}
	}()

	require.NoError(t, h.Shutdown(true))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := h.Wait(ctx)
	require.NoError(t, err)
	assert.NotZero(t, info.Signal)
}
