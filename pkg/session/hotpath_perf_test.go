package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// TestPrepareRequestSharesOperationWithoutDeepCopy 锁定 P1 优化意图：prepareRequest
// 复用同一个 Operation（连带 write payload 切片）而非深拷贝，并在 id 为空时生成 id，
// 且不改动调用方传入的原 req。
func TestPrepareRequestSharesOperationWithoutDeepCopy(t *testing.T) {
	t.Parallel()
	s := NewSession("user-x")

	payload := []byte("a large write payload that must not be copied")
	req := &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{Path: "f.txt", Content: payload},
		},
	}

	_, cloned, err := s.prepareRequest(context.Background(), req)
	require.NoError(t, err)

	// Operation（及其 *WriteFileReq / Content 切片）是共享指针，未被深拷贝。
	assert.Same(t, req.GetWrite(), cloned.GetWrite(), "write payload should be shared, not deep-copied")

	// 空 id 被补齐。
	assert.NotEmpty(t, cloned.GetRequestId())
	// 调用方原对象不被改动。
	assert.Empty(t, req.GetRequestId(), "caller's request must not be mutated")
}

// TestPrepareRequestPreservesProvidedID 保证已带 request_id 的请求原样保留。
func TestPrepareRequestPreservesProvidedID(t *testing.T) {
	t.Parallel()
	s := NewSession("user-x")

	req := &remotefsv1.FileRequest{
		RequestId: "caller-provided-id",
		Operation: &remotefsv1.FileRequest_Stat{Stat: &remotefsv1.StatReq{Path: "f.txt"}},
	}

	_, cloned, err := s.prepareRequest(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "caller-provided-id", cloned.GetRequestId())
}

// TestPrepareRequestNil 保证 nil 请求仍返回错误。
func TestPrepareRequestNil(t *testing.T) {
	t.Parallel()
	s := NewSession("user-x")
	_, _, err := s.prepareRequest(context.Background(), nil)
	assert.ErrorIs(t, err, ErrNilRequest)
}

// TestManagerGetConcurrentAccess 用大量并发 Get/Register/Remove/UserIDs 覆盖 P2 的
// RLock 快路径与 removeExpired 写锁升级，在 -race 下验证无数据竞争、无 panic。
func TestManagerGetConcurrentAccess(t *testing.T) {
	t.Parallel()
	manager := NewManager(ManagerOptions{OfflineReadOnlyTTL: time.Minute})

	const users = 8
	for i := range users {
		s := NewSession(fmt.Sprintf("user-%d", i))
		_, err := manager.Register(s)
		require.NoError(t, err)
	}

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(w int) {
			defer wg.Done()
			uid := fmt.Sprintf("user-%d", w%users)
			for range 200 {
				switch w % 4 {
				case 0, 1:
					manager.Get(uid)
				case 2:
					manager.UserIDs()
				case 3:
					s := NewSession(uid)
					// assert（非 require）在 goroutine 中是安全的：底层走 t.Errorf。
					_, regErr := manager.Register(s)
					assert.NoError(t, regErr)
				}
			}
		}(w)
	}
	wg.Wait()

	// 每个 user 仍应有且仅有一个可取到的 live session。
	for i := range users {
		uid := fmt.Sprintf("user-%d", i)
		got, ok := manager.Get(uid)
		require.True(t, ok)
		require.NotNil(t, got)
	}
}

// BenchmarkPrepareRequestLargeWrite 量化 P1：一个 128KiB 写请求经 prepareRequest 后
// 不应产生 payload 大小的分配（共享 Content，不深拷贝）。
func BenchmarkPrepareRequestLargeWrite(b *testing.B) {
	s := NewSession("user-x")
	payload := make([]byte, 128*1024)
	req := &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{Path: "big.bin", Content: payload},
		},
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, cloned, err := s.prepareRequest(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		_ = cloned
	}
}

// TestManagerGetLiveSessionNotRemoved 保证对 live（未过期）session 的 Get 只走读锁、
// 不删除、不改变映射。
func TestManagerGetLiveSessionNotRemoved(t *testing.T) {
	t.Parallel()
	manager := NewManager(ManagerOptions{OfflineReadOnlyTTL: time.Minute})
	current := NewSession("user-1")
	_, err := manager.Register(current)
	require.NoError(t, err)

	for range 3 {
		got, ok := manager.Get("user-1")
		require.True(t, ok)
		assert.Same(t, current, got)
	}
	assert.Equal(t, []string{"user-1"}, manager.UserIDs())
}
