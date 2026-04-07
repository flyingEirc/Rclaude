# Phase 4a — 写操作主链路 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> 上游 spec：[`docs/superpowers/specs/2026-04-07-phase4a-write-ops-design.md`](/e:/Rclaude/docs/superpowers/specs/2026-04-07-phase4a-write-ops-design.md)
> 上游 ROADMAP：[`docs/design/ROADMAP.md`](/e:/Rclaude/docs/design/ROADMAP.md) Phase 4
> 上一阶段：[`docs/exec-plan/completed/202604071046-phase3-server-fuse-mvp/`](/e:/Rclaude/docs/exec-plan/completed/202604071046-phase3-server-fuse-mvp/)

**Goal:** 让 FUSE 挂载点下的 Write/Create/Mkdir/Unlink/Rmdir/Rename/Truncate 以同步写透方式真实落到 daemon 工作区，errno 与本地文件系统语义一致；不引入任何缓存。

**Architecture:** daemon 端通过 per-path mutex 串行化写请求，写之前登记 self-write filter 抑制 fsnotify 回声，写后在响应里回填最新 FileInfo；server 端通过 view helper 把 FUSE 节点接口翻译成同步 RPC，收到响应后立刻把 FileInfo 合并到 session 本地树消除一致性窗口；错误用字符串关键字归类成 typed error 再翻成 syscall.Errno；端到端用进程内 transport 跑通 view→session→handle→真实临时目录的闭环。

**Tech Stack:** Go 1.25.2 / `google.golang.org/grpc` / `hanwen/go-fuse/v2` / `fsnotify` / `testify`。

---

## File Structure

### 新建

| 路径 | 职责 |
|---|---|
| `pkg/syncer/locker.go` | per-path mutex + refCount GC |
| `pkg/syncer/locker_test.go` | locker 单测 |
| `pkg/syncer/selfwrite.go` | self-write 过滤器（TTL + 惰性 GC + nowFn 注入） |
| `pkg/syncer/selfwrite_test.go` | filter 单测 |
| `pkg/syncer/handle_write.go` | 5 个写处理函数 + 公共 helper（formatErr / lstatToFileInfo） |
| `pkg/syncer/handle_write_test.go` | 5 op × 多类失败场景 |
| `internal/testutil/inmem_transport.go` | 进程内 daemon↔server transport，复用 MockConnectStream |
| `pkg/fusefs/inmem_e2e_test.go` | 7 个端到端写链路冒烟用例 |

### 修改

| 路径 | 改动 |
|---|---|
| `api/proto/remotefs/v1/remotefs.proto` | `WriteFileReq` 加 `int64 offset = 5`；新增 `TruncateReq`；`FileRequest.operation` 加 `truncate = 9` |
| `api/proto/remotefs/v1/remotefs.pb.go` / `remotefs_grpc.pb.go` | 重新生成 |
| `pkg/syncer/handle.go` | `HandleOptions` 加 `Locker / SelfWrites`；dispatch 增加 5 个写 op case |
| `pkg/syncer/watch.go` | `WatchOptions` 加 `SelfWrites`；事件投递前检查 |
| `pkg/syncer/watch_test.go` | 增加 self-write 抑制用例 |
| `pkg/syncer/daemon.go` | 装配 locker + selfWrites，传给 Handle/Watch；从 config 读 SelfWriteTTL |
| `pkg/config/server.go` | 加 `RequestTimeout time.Duration`，默认 10s |
| `pkg/config/daemon.go` | 加 `SelfWriteTTL time.Duration`，默认 2s |
| `pkg/session/manager.go` | 加 `ManagerOptions`、`requestTimeout` 字段、`RequestTimeout()` getter；`NewManager` 改为 variadic options |
| `pkg/session/manager_test.go` | 增加 RequestTimeout 单测 |
| `pkg/session/session.go` | 加 `ApplyWriteResult / ApplyDelete / ApplyRename` 三个方法 |
| `pkg/session/session_test.go` | 增加 Apply\* 单测 |
| `pkg/fusefs/view.go` | 新增 6 个 helper、错误哨兵、扩展 classifyError、新增 classifyRequestErr / withRequestTimeout |
| `pkg/fusefs/view_test.go` | 增加 classifyError 关键字表 + 6 个 helper 的 fake-session 单测 |
| `pkg/fusefs/mount_linux.go` | 在 `workspaceNode` 上实现 Write/Create/Mkdir/Unlink/Rmdir/Rename/Setattr；放开 Open；扩充 errnoFromError |
| `pkg/fusefs/mount_linux_test.go`（如不存在则创建） | 新增节点接口单测 |
| `app/server/main.go` | `session.NewManager` 调用改为传入 `ManagerOptions{RequestTimeout: cfg.RequestTimeout}` |

---

## Phase A — 协议与基础工具

### Task 1: proto 改动并重新生成

**Files:**
- Modify: `api/proto/remotefs/v1/remotefs.proto`
- Modify: `api/proto/remotefs/v1/remotefs.pb.go`（生成产物）
- Modify: `api/proto/remotefs/v1/remotefs_grpc.pb.go`（生成产物）

- [ ] **Step 1: 编辑 .proto**

把 `WriteFileReq` 改为：

```proto
message WriteFileReq {
  string path = 1;
  bytes content = 2;
  bool append = 3;
  uint32 mode = 4;
  int64 offset = 5;
}
```

在 `RenameReq` 之后插入：

```proto
message TruncateReq {
  string path = 1;
  int64 size = 2;
}
```

`FileRequest` 的 oneof 末尾增加：

```proto
TruncateReq truncate = 9;
```

- [ ] **Step 2: 重新生成 Go 代码**

Run: `make proto`（无 make 时回退 `protoc --go_out=. --go-grpc_out=. api/proto/remotefs/v1/remotefs.proto`）
Expected: `api/proto/remotefs/v1/remotefs.pb.go` 和 `remotefs_grpc.pb.go` 更新；`go build ./api/...` 通过

- [ ] **Step 3: 验证生成产物**

Run: `go build ./api/...`
Expected: 无错误

- [ ] **Step 4: 提交**

```bash
git add api/proto/remotefs/v1/remotefs.proto api/proto/remotefs/v1/remotefs.pb.go api/proto/remotefs/v1/remotefs_grpc.pb.go
git commit -m "feat(proto): add WriteFileReq.offset and TruncateReq for phase 4a"
```

---

### Task 2: pathLocker（按 path 串行化）

**Files:**
- Create: `pkg/syncer/locker.go`
- Test: `pkg/syncer/locker_test.go`

- [ ] **Step 1: 写失败的测试**

```go
package syncer

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathLocker_SerializesSamePath(t *testing.T) {
	l := newPathLocker()

	var (
		mu       sync.Mutex
		order    []int
		started1 = make(chan struct{})
	)
	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		unlock := l.Lock("a/b")
		close(started1)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
		unlock()
	}()

	go func() {
		defer wg.Done()
		<-started1
		unlock := l.Lock("a/b")
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		unlock()
	}()

	wg.Wait()
	require.Equal(t, []int{1, 2}, order)
}

func TestPathLocker_ConcurrentDifferentPaths(t *testing.T) {
	l := newPathLocker()

	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	wg := sync.WaitGroup{}
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := l.Lock("p" + string(rune('0'+i)))
			c := concurrent.Add(1)
			for {
				old := maxConcurrent.Load()
				if c <= old || maxConcurrent.CompareAndSwap(old, c) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			concurrent.Add(-1)
			unlock()
		}()
	}
	wg.Wait()
	assert.Greater(t, maxConcurrent.Load(), int32(1), "different paths should run concurrently")
}

func TestPathLocker_RefCountReclaim(t *testing.T) {
	l := newPathLocker()
	unlock := l.Lock("x")
	unlock()
	require.Equal(t, 0, l.size(), "entry should be reclaimed after unlock")
}

func TestPathLocker_NormalizesSlash(t *testing.T) {
	l := newPathLocker()
	unlock1 := l.Lock(`a\b`)
	defer unlock1()
	// 第二次取 forward-slash 形式应该被视为同一把锁；
	// 通过另一 goroutine 检测它阻塞。
	gotLock := make(chan struct{})
	go func() {
		u := l.Lock("a/b")
		close(gotLock)
		u()
	}()
	select {
	case <-gotLock:
		t.Fatal("expected lock to block on normalized path")
	case <-time.After(20 * time.Millisecond):
	}

	// 排序兜底，避免编译警告
	_ = sort.IntsAreSorted(nil)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestPathLocker -count=1 ./pkg/syncer/...`
Expected: 编译失败（`newPathLocker undefined`、`l.size undefined`）

- [ ] **Step 3: 实现 locker.go**

Create `pkg/syncer/locker.go`:

```go
package syncer

import (
	"sync"

	"flyingEirc/Rclaude/pkg/safepath"
)

// pathLocker serializes concurrent operations on the same path while
// allowing operations on different paths to proceed in parallel.
type pathLocker struct {
	mu    sync.Mutex
	locks map[string]*pathEntry
}

type pathEntry struct {
	mu       sync.Mutex
	refCount int
}

func newPathLocker() *pathLocker {
	return &pathLocker{locks: make(map[string]*pathEntry)}
}

// Lock acquires the mutex for the normalized form of path and returns the
// release closure. Calling the closure decrements the refCount and reclaims
// the entry from the map when no holders remain, preventing unbounded growth.
func (l *pathLocker) Lock(path string) func() {
	key := safepath.ToSlash(path)

	l.mu.Lock()
	entry, ok := l.locks[key]
	if !ok {
		entry = &pathEntry{}
		l.locks[key] = entry
	}
	entry.refCount++
	l.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		l.mu.Lock()
		entry.refCount--
		if entry.refCount == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

// size returns the number of live entries (test-only helper).
func (l *pathLocker) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}

// LockMany acquires multiple paths in deterministic order to avoid deadlocks.
// Used by handleRename to lock both old and new paths simultaneously.
func (l *pathLocker) LockMany(paths ...string) func() {
	keys := make([]string, len(paths))
	for i, p := range paths {
		keys[i] = safepath.ToSlash(p)
	}
	dedupSortKeys(&keys)

	releases := make([]func(), 0, len(keys))
	for _, k := range keys {
		releases = append(releases, l.Lock(k))
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
}

func dedupSortKeys(keys *[]string) {
	sortStrings(*keys)
	out := (*keys)[:0]
	var prev string
	for i, k := range *keys {
		if i == 0 || k != prev {
			out = append(out, k)
			prev = k
		}
	}
	*keys = out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -run TestPathLocker -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/locker.go pkg/syncer/locker_test.go
git commit -m "feat(syncer): add per-path locker for write serialization"
```

---

### Task 3: selfWriteFilter（self-write 抑制）

**Files:**
- Create: `pkg/syncer/selfwrite.go`
- Test: `pkg/syncer/selfwrite_test.go`

- [ ] **Step 1: 写失败的测试**

```go
package syncer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfWriteFilter_RememberWithinTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	f := newSelfWriteFilter(2 * time.Second)
	f.nowFn = func() time.Time { return now }

	f.Remember("a/b.txt")
	now = now.Add(time.Second)
	require.True(t, f.ShouldSuppress("a/b.txt"))
}

func TestSelfWriteFilter_ExpiredAfterTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	f := newSelfWriteFilter(2 * time.Second)
	f.nowFn = func() time.Time { return now }

	f.Remember("a/b.txt")
	now = now.Add(3 * time.Second)
	assert.False(t, f.ShouldSuppress("a/b.txt"))
}

func TestSelfWriteFilter_MultiplePaths(t *testing.T) {
	now := time.Unix(1000, 0)
	f := newSelfWriteFilter(2 * time.Second)
	f.nowFn = func() time.Time { return now }

	f.Remember("a/old.txt", "a/new.txt")
	assert.True(t, f.ShouldSuppress("a/old.txt"))
	assert.True(t, f.ShouldSuppress("a/new.txt"))
}

func TestSelfWriteFilter_LazyGC(t *testing.T) {
	now := time.Unix(1000, 0)
	f := newSelfWriteFilter(time.Second)
	f.nowFn = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		f.Remember("p" + string(rune('a'+i)))
	}
	require.Equal(t, 5, f.size())

	now = now.Add(2 * time.Second)
	_ = f.ShouldSuppress("never-existed")
	assert.Equal(t, 0, f.size(), "expired entries should be reclaimed lazily")
}

func TestSelfWriteFilter_RememberRefreshesExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	f := newSelfWriteFilter(2 * time.Second)
	f.nowFn = func() time.Time { return now }

	f.Remember("a")
	now = now.Add(time.Second)
	f.Remember("a")
	now = now.Add(time.Second + 500*time.Millisecond)
	assert.True(t, f.ShouldSuppress("a"), "second Remember should refresh expiry")
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestSelfWriteFilter -count=1 ./pkg/syncer/...`
Expected: 编译失败

- [ ] **Step 3: 实现 selfwrite.go**

Create `pkg/syncer/selfwrite.go`:

```go
package syncer

import (
	"sync"
	"time"

	"flyingEirc/Rclaude/pkg/safepath"
)

// selfWriteFilter remembers paths the daemon recently wrote itself so that
// fsnotify echo events for those paths can be suppressed for a short window.
type selfWriteFilter struct {
	ttl   time.Duration
	nowFn func() time.Time

	mu      sync.Mutex
	entries map[string]time.Time
}

func newSelfWriteFilter(ttl time.Duration) *selfWriteFilter {
	return &selfWriteFilter{
		ttl:     ttl,
		nowFn:   time.Now,
		entries: make(map[string]time.Time),
	}
}

// Remember marks each path as a self-write for the next ttl interval.
// A second Remember on the same path refreshes the expiry.
func (f *selfWriteFilter) Remember(paths ...string) {
	if f == nil || f.ttl <= 0 {
		return
	}
	expiry := f.nowFn().Add(f.ttl)

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range paths {
		f.entries[safepath.ToSlash(p)] = expiry
	}
}

// ShouldSuppress reports whether the given path is currently within the
// suppression window. It also lazily reclaims expired entries.
func (f *selfWriteFilter) ShouldSuppress(path string) bool {
	if f == nil || f.ttl <= 0 {
		return false
	}
	now := f.nowFn()
	key := safepath.ToSlash(path)

	f.mu.Lock()
	defer f.mu.Unlock()

	for k, exp := range f.entries {
		if !exp.After(now) {
			delete(f.entries, k)
		}
	}

	exp, ok := f.entries[key]
	return ok && exp.After(now)
}

// size returns the number of live entries (test-only helper).
func (f *selfWriteFilter) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -run TestSelfWriteFilter -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/selfwrite.go pkg/syncer/selfwrite_test.go
git commit -m "feat(syncer): add self-write filter to suppress fsnotify echo"
```

---

## Phase B — Daemon 写处理

### Task 4: handle_write.go 公共框架（HandleOptions + helper）

**Files:**
- Modify: `pkg/syncer/handle.go`（HandleOptions 加字段；新建 dispatch case 留到 Task 8）
- Create: `pkg/syncer/handle_write.go`（仅放 helper：formatErr / formatRenameErr / lstatToFileInfo / writeDeps）

- [ ] **Step 1: 修改 HandleOptions（加字段，旧调用零影响）**

`pkg/syncer/handle.go` 现有 `HandleOptions` 改为：

```go
type HandleOptions struct {
	Root        string
	MaxReadSize int64
	Locker      *pathLocker
	SelfWrites  *selfWriteFilter
}
```

不动 `Handle` 函数体的 dispatch（Task 8 一起改）。

- [ ] **Step 2: 写 helper 文件**

Create `pkg/syncer/handle_write.go`:

```go
package syncer

import (
	"fmt"
	"io/fs"
	"os"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/safepath"
)

// writeDeps bundles the per-process collaborators that all write handlers need.
type writeDeps struct {
	locker     *pathLocker
	selfWrites *selfWriteFilter
}

func depsFromOptions(opts HandleOptions) writeDeps {
	return writeDeps{locker: opts.Locker, selfWrites: opts.SelfWrites}
}

// formatErr renders the daemon error string with a stable prefix that the
// view layer's classifyError keyword table relies on.
func formatErr(op, path string, err error) string {
	return fmt.Sprintf("syncer: %s %q: %v", op, path, err)
}

func formatRenameErr(oldPath, newPath string, err error) string {
	return fmt.Sprintf("syncer: rename %q->%q: %v", oldPath, newPath, err)
}

// lstatToFileInfo turns an absolute path on disk back into a protocol FileInfo
// using the relative path that the request used.
func lstatToFileInfo(abs, rel string) (*remotefsv1.FileInfo, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	return fileInfoFromFS(rel, fi), nil
}

func successWithInfo(reqID string, info *remotefsv1.FileInfo) *remotefsv1.FileResponse {
	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
		Result:    &remotefsv1.FileResponse_Info{Info: info},
	}
}

func successNoResult(reqID string) *remotefsv1.FileResponse {
	return &remotefsv1.FileResponse{RequestId: reqID, Success: true}
}

// safeAcquire centralises the safepath check + locker acquisition + self-write
// registration order so each handler can stay focused on its os.* call.
func safeAcquire(opts HandleOptions, deps writeDeps, rel string) (string, func(), error) {
	abs, err := safepath.Join(opts.Root, rel)
	if err != nil {
		return "", nil, err
	}
	unlock := func() {}
	if deps.locker != nil {
		unlock = deps.locker.Lock(rel)
	}
	if deps.selfWrites != nil {
		deps.selfWrites.Remember(rel)
	}
	return abs, unlock, nil
}

// fileInfoFromOSStat is a thin wrapper used by handlers that already hold an
// fs.FileInfo (e.g. the Lstat after MkdirAll).
func fileInfoFromOSStat(rel string, fi fs.FileInfo) *remotefsv1.FileInfo {
	return fileInfoFromFS(rel, fi)
}
```

- [ ] **Step 3: 验证编译**

Run: `go build ./pkg/syncer/...`
Expected: 无错误

- [ ] **Step 4: 验证现有测试不回归**

Run: `go test -count=1 ./pkg/syncer/...`
Expected: PASS（旧 read/stat/list_dir 的测试不受影响；新 write 测试还没加）

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/handle.go pkg/syncer/handle_write.go
git commit -m "feat(syncer): add write handler scaffolding (deps, helpers)"
```

---

### Task 5: handleWrite

**Files:**
- Modify: `pkg/syncer/handle_write.go`
- Modify: `pkg/syncer/handle_write_test.go`（新建）

- [ ] **Step 1: 写失败的测试**

Create `pkg/syncer/handle_write_test.go`:

```go
package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func newWriteOpts(t *testing.T) (HandleOptions, writeDeps, string) {
	t.Helper()
	root := t.TempDir()
	opts := HandleOptions{
		Root:       root,
		Locker:     newPathLocker(),
		SelfWrites: newSelfWriteFilter(2 * time.Second),
	}
	return opts, depsFromOptions(opts), root
}

func writeReq(rel string, content []byte, offset int64, append bool) *remotefsv1.FileRequest {
	return &remotefsv1.FileRequest{
		RequestId: "req-1",
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{
				Path:    rel,
				Content: content,
				Offset:  offset,
				Append:  append,
			},
		},
	}
}

func TestHandleWrite_CreatesFileAndReturnsInfo(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	resp := Handle(writeReq("a.txt", []byte("hello"), 0, false), opts)

	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())
	require.NotNil(t, resp.GetInfo())
	assert.Equal(t, "a.txt", resp.GetInfo().GetPath())
	assert.EqualValues(t, 5, resp.GetInfo().GetSize())

	got, err := os.ReadFile(filepath.Join(root, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestHandleWrite_OffsetWriteAt(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world"), 0o644))

	resp := Handle(writeReq("a.txt", []byte("XYZ"), 6, false), opts)
	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())

	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	assert.Equal(t, "hello XYZld", string(got))
}

func TestHandleWrite_AppendIgnoresOffset(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo"), 0o644))

	resp := Handle(writeReq("a.txt", []byte("bar"), 99, true), opts)
	require.True(t, resp.GetSuccess())

	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	assert.Equal(t, "foobar", string(got))
}

func TestHandleWrite_NegativeOffsetIsInvalid(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(writeReq("a.txt", []byte("x"), -1, false), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "invalid")
}

func TestHandleWrite_UnsafePath(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(writeReq("../escape", []byte("x"), 0, false), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "unsafe path")
}

func TestHandleWrite_RemembersSelfWrite(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(writeReq("a.txt", []byte("hi"), 0, false), opts)
	require.True(t, resp.GetSuccess())
	assert.True(t, opts.SelfWrites.ShouldSuppress("a.txt"))
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestHandleWrite -count=1 ./pkg/syncer/...`
Expected: 失败（dispatch 还没接 write，返回 "operation not supported"）

- [ ] **Step 3: 实现 handleWrite + 临时挂到 dispatch**

在 `pkg/syncer/handle_write.go` 末尾追加：

```go
// handleWrite handles WriteFileReq. Append takes precedence over offset.
// On success the response carries the post-write FileInfo so the server side
// session tree can be updated immediately.
func handleWrite(reqID string, r *remotefsv1.WriteFileReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil write request")
	}
	if !r.GetAppend() && r.GetOffset() < 0 {
		return errResponse(reqID, fmt.Sprintf("syncer: write %q: invalid argument: negative offset", r.GetPath()))
	}

	abs, unlock, err := safeAcquire(opts, deps, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	defer unlock()

	flag := os.O_RDWR | os.O_CREATE
	if r.GetAppend() {
		flag = os.O_WRONLY | os.O_APPEND | os.O_CREATE
	}

	f, err := os.OpenFile(abs, flag, 0o644) //nolint:gosec // safepath validated
	if err != nil {
		return errResponse(reqID, formatErr("write", r.GetPath(), err))
	}
	defer f.Close()

	if r.GetAppend() {
		if _, err := f.Write(r.GetContent()); err != nil {
			return errResponse(reqID, formatErr("write", r.GetPath(), err))
		}
	} else {
		if _, err := f.WriteAt(r.GetContent(), r.GetOffset()); err != nil {
			return errResponse(reqID, formatErr("write", r.GetPath(), err))
		}
	}

	if err := f.Sync(); err != nil { //nolint:revive // best-effort durability
		return errResponse(reqID, formatErr("write", r.GetPath(), err))
	}

	info, err := lstatToFileInfo(abs, r.GetPath())
	if err != nil {
		return errResponse(reqID, formatErr("write", r.GetPath(), err))
	}
	return successWithInfo(reqID, info)
}
```

修改 `pkg/syncer/handle.go` 的 `Handle` dispatch，把 `*remotefsv1.FileRequest_Write` 单独 case 出来：

```go
case *remotefsv1.FileRequest_Write:
	return handleWrite(reqID, op.Write, opts, depsFromOptions(opts))
```

并把原来落到 "phase 2" 错误分支里的 Write 移除。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -run TestHandleWrite -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/handle.go pkg/syncer/handle_write.go pkg/syncer/handle_write_test.go
git commit -m "feat(syncer): implement handleWrite with offset and append"
```

---

### Task 6: handleMkdir / handleDelete / handleTruncate

**Files:**
- Modify: `pkg/syncer/handle_write.go`
- Modify: `pkg/syncer/handle.go`（dispatch 增加 3 个 case）
- Modify: `pkg/syncer/handle_write_test.go`（新增 3 组测试）

- [ ] **Step 1: 写失败的测试**

在 `handle_write_test.go` 末尾追加：

```go
func mkdirReq(rel string, recursive bool) *remotefsv1.FileRequest {
	return &remotefsv1.FileRequest{
		RequestId: "req-mk",
		Operation: &remotefsv1.FileRequest_Mkdir{
			Mkdir: &remotefsv1.MkdirReq{Path: rel, Recursive: recursive},
		},
	}
}

func deleteReq(rel string) *remotefsv1.FileRequest {
	return &remotefsv1.FileRequest{
		RequestId: "req-del",
		Operation: &remotefsv1.FileRequest_Delete{
			Delete: &remotefsv1.DeleteReq{Path: rel},
		},
	}
}

func truncateReq(rel string, size int64) *remotefsv1.FileRequest {
	return &remotefsv1.FileRequest{
		RequestId: "req-tr",
		Operation: &remotefsv1.FileRequest_Truncate{
			Truncate: &remotefsv1.TruncateReq{Path: rel, Size: size},
		},
	}
}

func TestHandleMkdir_NonRecursive(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	resp := Handle(mkdirReq("d", false), opts)
	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())
	require.NotNil(t, resp.GetInfo())
	assert.True(t, resp.GetInfo().GetIsDir())

	stat, err := os.Stat(filepath.Join(root, "d"))
	require.NoError(t, err)
	assert.True(t, stat.IsDir())
}

func TestHandleMkdir_RecursiveCreatesParents(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	resp := Handle(mkdirReq("a/b/c", true), opts)
	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())

	stat, err := os.Stat(filepath.Join(root, "a", "b", "c"))
	require.NoError(t, err)
	assert.True(t, stat.IsDir())
}

func TestHandleMkdir_AlreadyExists(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "d"), 0o755))
	resp := Handle(mkdirReq("d", false), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "exists")
}

func TestHandleDelete_File(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte("hi"), 0o644))
	resp := Handle(deleteReq("x.txt"), opts)
	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())
	assert.Nil(t, resp.GetInfo())

	_, err := os.Stat(filepath.Join(root, "x.txt"))
	assert.True(t, os.IsNotExist(err))
}

func TestHandleDelete_EmptyDir(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "d"), 0o755))
	resp := Handle(deleteReq("d"), opts)
	require.True(t, resp.GetSuccess())
}

func TestHandleDelete_NonEmptyDir(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "d", "x"), []byte("x"), 0o644))
	resp := Handle(deleteReq("d"), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "not empty")
}

func TestHandleDelete_NotFound(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(deleteReq("nope"), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "no such file")
}

func TestHandleTruncate_ShrinksFile(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world"), 0o644))
	resp := Handle(truncateReq("a.txt", 5), opts)
	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())
	require.NotNil(t, resp.GetInfo())
	assert.EqualValues(t, 5, resp.GetInfo().GetSize())

	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	assert.Equal(t, "hello", string(got))
}

func TestHandleTruncate_NegativeSize(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(truncateReq("a.txt", -1), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "invalid")
}

func TestHandleTruncate_NotFound(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(truncateReq("missing", 0), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "no such file")
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestHandleMkdir|TestHandleDelete|TestHandleTruncate" -count=1 ./pkg/syncer/...`
Expected: 失败（dispatch 仍然返回 not supported）

- [ ] **Step 3: 实现三个 handler**

在 `handle_write.go` 末尾追加：

```go
func handleMkdir(reqID string, r *remotefsv1.MkdirReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil mkdir request")
	}
	abs, unlock, err := safeAcquire(opts, deps, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	defer unlock()

	if r.GetRecursive() {
		err = os.MkdirAll(abs, 0o755)
	} else {
		err = os.Mkdir(abs, 0o755)
	}
	if err != nil {
		return errResponse(reqID, formatErr("mkdir", r.GetPath(), err))
	}

	info, err := lstatToFileInfo(abs, r.GetPath())
	if err != nil {
		return errResponse(reqID, formatErr("mkdir", r.GetPath(), err))
	}
	return successWithInfo(reqID, info)
}

func handleDelete(reqID string, r *remotefsv1.DeleteReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil delete request")
	}
	abs, unlock, err := safeAcquire(opts, deps, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	defer unlock()

	if err := os.Remove(abs); err != nil {
		return errResponse(reqID, formatErr("delete", r.GetPath(), err))
	}
	return successNoResult(reqID)
}

func handleTruncate(reqID string, r *remotefsv1.TruncateReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil truncate request")
	}
	if r.GetSize() < 0 {
		return errResponse(reqID, fmt.Sprintf("syncer: truncate %q: invalid argument: negative size", r.GetPath()))
	}
	abs, unlock, err := safeAcquire(opts, deps, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	defer unlock()

	if err := os.Truncate(abs, r.GetSize()); err != nil {
		return errResponse(reqID, formatErr("truncate", r.GetPath(), err))
	}

	info, err := lstatToFileInfo(abs, r.GetPath())
	if err != nil {
		return errResponse(reqID, formatErr("truncate", r.GetPath(), err))
	}
	return successWithInfo(reqID, info)
}
```

修改 `handle.go` 的 `Handle` dispatch，新增三个 case：

```go
case *remotefsv1.FileRequest_Mkdir:
	return handleMkdir(reqID, op.Mkdir, opts, depsFromOptions(opts))
case *remotefsv1.FileRequest_Delete:
	return handleDelete(reqID, op.Delete, opts, depsFromOptions(opts))
case *remotefsv1.FileRequest_Truncate:
	return handleTruncate(reqID, op.Truncate, opts, depsFromOptions(opts))
```

并把对应 op 从原 "not supported" 兜底分支移除。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/handle.go pkg/syncer/handle_write.go pkg/syncer/handle_write_test.go
git commit -m "feat(syncer): implement handleMkdir / handleDelete / handleTruncate"
```

---

### Task 7: handleRename（双 path 加锁）

**Files:**
- Modify: `pkg/syncer/handle_write.go`
- Modify: `pkg/syncer/handle.go`
- Modify: `pkg/syncer/handle_write_test.go`

- [ ] **Step 1: 写失败的测试**

在 `handle_write_test.go` 末尾追加：

```go
func renameReq(oldRel, newRel string) *remotefsv1.FileRequest {
	return &remotefsv1.FileRequest{
		RequestId: "req-rn",
		Operation: &remotefsv1.FileRequest_Rename{
			Rename: &remotefsv1.RenameReq{OldPath: oldRel, NewPath: newRel},
		},
	}
}

func TestHandleRename_AcrossDirs(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "x"), []byte("hi"), 0o644))

	resp := Handle(renameReq("a/x", "b/x"), opts)
	require.True(t, resp.GetSuccess(), "error: %s", resp.GetError())
	require.NotNil(t, resp.GetInfo())
	assert.Equal(t, "b/x", resp.GetInfo().GetPath())

	_, err := os.Stat(filepath.Join(root, "a", "x"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "b", "x"))
	assert.NoError(t, err)
}

func TestHandleRename_SourceNotFound(t *testing.T) {
	opts, _, _ := newWriteOpts(t)
	resp := Handle(renameReq("a", "b"), opts)
	require.False(t, resp.GetSuccess())
	assert.Contains(t, strings.ToLower(resp.GetError()), "no such file")
}

func TestHandleRename_RemembersBothPaths(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o644))
	resp := Handle(renameReq("a", "b"), opts)
	require.True(t, resp.GetSuccess())
	assert.True(t, opts.SelfWrites.ShouldSuppress("a"))
	assert.True(t, opts.SelfWrites.ShouldSuppress("b"))
}

func TestHandleRename_NoDeadlockOnReversePairs(t *testing.T) {
	opts, _, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "x"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "y"), []byte("y"), 0o644))

	done := make(chan struct{})
	go func() {
		Handle(renameReq("x", "y"), opts)
		Handle(renameReq("y", "x"), opts)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rename pair deadlocked")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestHandleRename -count=1 ./pkg/syncer/...`
Expected: 失败

- [ ] **Step 3: 实现 handleRename（用 LockMany）**

在 `handle_write.go` 末尾追加：

```go
func handleRename(reqID string, r *remotefsv1.RenameReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil rename request")
	}
	absOld, err := safepath.Join(opts.Root, r.GetOldPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	absNew, err := safepath.Join(opts.Root, r.GetNewPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}

	unlock := func() {}
	if deps.locker != nil {
		unlock = deps.locker.LockMany(r.GetOldPath(), r.GetNewPath())
	}
	defer unlock()

	if deps.selfWrites != nil {
		deps.selfWrites.Remember(r.GetOldPath(), r.GetNewPath())
	}

	if err := os.Rename(absOld, absNew); err != nil {
		return errResponse(reqID, formatRenameErr(r.GetOldPath(), r.GetNewPath(), err))
	}

	info, err := lstatToFileInfo(absNew, r.GetNewPath())
	if err != nil {
		return errResponse(reqID, formatRenameErr(r.GetOldPath(), r.GetNewPath(), err))
	}
	return successWithInfo(reqID, info)
}
```

修改 `handle.go` 的 dispatch，加 case：

```go
case *remotefsv1.FileRequest_Rename:
	return handleRename(reqID, op.Rename, opts, depsFromOptions(opts))
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/handle.go pkg/syncer/handle_write.go pkg/syncer/handle_write_test.go
git commit -m "feat(syncer): implement handleRename with dual-path locking"
```

---

### Task 8: 清理 dispatch + 校验 5 op 全通

**Files:**
- Modify: `pkg/syncer/handle.go`

- [ ] **Step 1: 清理 "phase 2 not supported" 兜底分支**

确认 `pkg/syncer/handle.go` 的 `Handle` 函数 dispatch 现在长这样：

```go
switch op := req.GetOperation().(type) {
case *remotefsv1.FileRequest_Read:
	return handleRead(reqID, op.Read, opts)
case *remotefsv1.FileRequest_Stat:
	return handleStat(reqID, op.Stat, opts)
case *remotefsv1.FileRequest_ListDir:
	return handleListDir(reqID, op.ListDir, opts)
case *remotefsv1.FileRequest_Write:
	return handleWrite(reqID, op.Write, opts, depsFromOptions(opts))
case *remotefsv1.FileRequest_Mkdir:
	return handleMkdir(reqID, op.Mkdir, opts, depsFromOptions(opts))
case *remotefsv1.FileRequest_Delete:
	return handleDelete(reqID, op.Delete, opts, depsFromOptions(opts))
case *remotefsv1.FileRequest_Rename:
	return handleRename(reqID, op.Rename, opts, depsFromOptions(opts))
case *remotefsv1.FileRequest_Truncate:
	return handleTruncate(reqID, op.Truncate, opts, depsFromOptions(opts))
default:
	return errResponse(reqID, "syncer: unknown operation")
}
```

「phase 2 not supported」分支整体删除（已经被五个 case 替代）。

- [ ] **Step 2: 跑全套 syncer 测试**

Run: `go test -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 3: 提交**

```bash
git add pkg/syncer/handle.go
git commit -m "refactor(syncer): drop phase-2 not-supported fallback"
```

---

## Phase C — Daemon Watch 抑制 + 装配

### Task 9: watch.go 接受 SelfWrites 并抑制

**Files:**
- Modify: `pkg/syncer/watch.go`
- Modify: `pkg/syncer/watch_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/syncer/watch_test.go` 末尾追加（保留现有 import；如缺失则补 `time`、`require`）：

```go
func TestWatch_SuppressesSelfWrite(t *testing.T) {
	root := t.TempDir()
	events := make(chan *remotefsv1.FileChange, 8)
	filter := newSelfWriteFilter(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, WatchOptions{
			Root:       root,
			Events:     events,
			SelfWrites: filter,
		})
	}()

	// 等 watcher 就绪
	time.Sleep(100 * time.Millisecond)

	filter.Remember("self.txt")
	require.NoError(t, os.WriteFile(filepath.Join(root, "self.txt"), []byte("x"), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(root, "other.txt"), []byte("x"), 0o644))

	deadline := time.After(800 * time.Millisecond)
	var got []*remotefsv1.FileChange
loop:
	for {
		select {
		case ev := <-events:
			got = append(got, ev)
		case <-deadline:
			break loop
		}
	}

	cancel()
	<-done

	for _, ev := range got {
		assert.NotEqual(t, "self.txt", ev.GetFile().GetPath(), "self.txt should be suppressed")
	}
	var sawOther bool
	for _, ev := range got {
		if ev.GetFile().GetPath() == "other.txt" {
			sawOther = true
		}
	}
	assert.True(t, sawOther, "non-self path should still emit events")
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestWatch_SuppressesSelfWrite -count=1 ./pkg/syncer/...`
Expected: 编译失败（`WatchOptions.SelfWrites undefined`）

- [ ] **Step 3: 实现 SelfWrites 字段与抑制**

在 `pkg/syncer/watch.go` 中，`WatchOptions` 增加字段：

```go
type WatchOptions struct {
	Root       string
	Excludes   []string
	Events     chan<- *remotefsv1.FileChange
	Logger     *slog.Logger
	SelfWrites *selfWriteFilter
}
```

在 `watchChangeFromEvent` 中（`matchExclude` 检查之后、`toChangeType` 之前）插入：

```go
if opts.SelfWrites != nil && opts.SelfWrites.ShouldSuppress(rel) {
	return nil, false
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/syncer/watch.go pkg/syncer/watch_test.go
git commit -m "feat(syncer): suppress self-write events in watch loop"
```

---

### Task 10: pkg/config 增加 RequestTimeout / SelfWriteTTL

**Files:**
- Modify: `pkg/config/server.go`
- Modify: `pkg/config/daemon.go`
- Modify: 对应 `*_test.go`（增量 default 校验）

- [ ] **Step 1: 阅读现有 config 文件**

Run: `cat pkg/config/server.go pkg/config/daemon.go`（用 Read 工具）
确认：现有结构体名、tag 风格、default 注入位置。

- [ ] **Step 2: 写默认值断言测试**

在对应的 `*_test.go` 末尾追加：

```go
func TestServerConfig_DefaultRequestTimeout(t *testing.T) {
	cfg, err := LoadServerFromBytes([]byte("{}")) // 用现有 LoadServer 入口；具体 fn 名按现有命名
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, cfg.RequestTimeout)
}

func TestDaemonConfig_DefaultSelfWriteTTL(t *testing.T) {
	cfg, err := LoadDaemonFromBytes([]byte("{}"))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, cfg.SelfWriteTTL)
}
```

> 测试函数名按现有 `pkg/config/*_test.go` 的 import 与 helper 命名调整；如果现有 loader 是 `LoadServer(filename string)` 形式，先在 `_test.go` 里写一个临时文件再 LoadServer。

- [ ] **Step 3: 运行测试确认失败**

Run: `go test -count=1 ./pkg/config/...`
Expected: 失败（字段不存在）

- [ ] **Step 4: 加字段与默认值**

`pkg/config/server.go` 的 ServerConfig 增加：

```go
RequestTimeout time.Duration `mapstructure:"request_timeout"`
```

加载/默认逻辑（在现有 default 注入处）补：

```go
if cfg.RequestTimeout <= 0 {
	cfg.RequestTimeout = 10 * time.Second
}
```

`pkg/config/daemon.go` 的 DaemonConfig 增加：

```go
SelfWriteTTL time.Duration `mapstructure:"self_write_ttl"`
```

加载/默认：

```go
if cfg.SelfWriteTTL <= 0 {
	cfg.SelfWriteTTL = 2 * time.Second
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test -count=1 ./pkg/config/...`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add pkg/config/server.go pkg/config/daemon.go pkg/config/server_test.go pkg/config/daemon_test.go
git commit -m "feat(config): add RequestTimeout (server) and SelfWriteTTL (daemon)"
```

---

### Task 11: pkg/syncer/daemon.go 装配 locker + selfWrites

**Files:**
- Modify: `pkg/syncer/daemon.go`

- [ ] **Step 1: 阅读现有 daemon.go**

Run: 用 Read 看 `pkg/syncer/daemon.go` 当前结构。重点找：`Handle` 调用点、`Watch` 调用点、`RunOptions` 定义。

- [ ] **Step 2: RunOptions 加 SelfWriteTTL**

在 `RunOptions` 结构体里加：

```go
// SelfWriteTTL controls how long the daemon suppresses fsnotify events for
// paths it has just written itself. Defaults to 2s when zero.
SelfWriteTTL time.Duration
```

- [ ] **Step 3: 在 Run 函数顶部构造单例**

在 daemon.Run（或同等启动函数）创建 watch 与 handle 调用之前插入：

```go
ttl := opts.SelfWriteTTL
if ttl <= 0 {
	ttl = 2 * time.Second
}
locker := newPathLocker()
selfWrites := newSelfWriteFilter(ttl)
```

- [ ] **Step 4: 把 locker / selfWrites 透传给 Handle 与 Watch**

- 把 `HandleOptions{Root: ..., MaxReadSize: ...}` 改为同时传 `Locker: locker, SelfWrites: selfWrites`
- 把 `WatchOptions{Root: ..., Events: ...}` 改为同时传 `SelfWrites: selfWrites`

- [ ] **Step 5: 跑全套 syncer 测试**

Run: `go test -race -count=1 ./pkg/syncer/...`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add pkg/syncer/daemon.go
git commit -m "feat(syncer): wire locker and self-write filter into daemon Run"
```

---

## Phase D — Server Manager + Apply\*

### Task 12: ManagerOptions + RequestTimeout

**Files:**
- Modify: `pkg/session/manager.go`
- Modify: `pkg/session/manager_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/session/manager_test.go` 末尾追加：

```go
func TestManager_RequestTimeout_Default(t *testing.T) {
	m := NewManager()
	assert.Equal(t, time.Duration(0), m.RequestTimeout())
}

func TestManager_RequestTimeout_Custom(t *testing.T) {
	m := NewManager(ManagerOptions{RequestTimeout: 5 * time.Second})
	assert.Equal(t, 5*time.Second, m.RequestTimeout())
}
```

（需要 import `time` 与 `assert`，按文件现有风格。）

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestManager_RequestTimeout -count=1 ./pkg/session/...`
Expected: 编译失败

- [ ] **Step 3: 实现**

修改 `pkg/session/manager.go`：

```go
type Manager struct {
	mu             sync.RWMutex
	sessions       map[string]*Session
	requestTimeout time.Duration
}

// ManagerOptions configures Manager behaviour. Pass at most one to NewManager.
type ManagerOptions struct {
	RequestTimeout time.Duration
}

// NewManager constructs an empty session manager. The variadic options form
// keeps existing zero-arg call sites compatible while allowing the server
// startup path to inject a non-zero RequestTimeout.
func NewManager(opts ...ManagerOptions) *Manager {
	o := ManagerOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	return &Manager{
		sessions:       make(map[string]*Session),
		requestTimeout: o.RequestTimeout,
	}
}

// RequestTimeout returns the configured per-RPC default timeout. A zero value
// means "no timeout" and view-layer helpers will pass through the parent ctx
// unchanged.
func (m *Manager) RequestTimeout() time.Duration {
	if m == nil {
		return 0
	}
	return m.requestTimeout
}
```

加 `import "time"`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -count=1 ./pkg/session/...`
Expected: PASS（旧的零参 NewManager 调用全部保持有效）

- [ ] **Step 5: 提交**

```bash
git add pkg/session/manager.go pkg/session/manager_test.go
git commit -m "feat(session): add ManagerOptions and RequestTimeout getter"
```

---

### Task 13: Session.ApplyWriteResult / ApplyDelete / ApplyRename

**Files:**
- Modify: `pkg/session/session.go`
- Modify: `pkg/session/session_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/session/session_test.go` 末尾追加：

```go
func TestSession_ApplyWriteResult_VisibleImmediately(t *testing.T) {
	s := NewSession("u1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	s.ApplyWriteResult(&remotefsv1.FileInfo{
		Path: "a/b.txt", Size: 5, IsDir: false, Mode: 0o644,
	})

	info, ok := s.Lookup("a/b.txt")
	require.True(t, ok)
	assert.EqualValues(t, 5, info.GetSize())
}

func TestSession_ApplyDelete(t *testing.T) {
	s := NewSession("u1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{
			Files: []*remotefsv1.FileInfo{{Path: "x", Size: 1}},
		}},
	}))

	s.ApplyDelete("x")

	_, ok := s.Lookup("x")
	assert.False(t, ok)
}

func TestSession_ApplyRename(t *testing.T) {
	s := NewSession("u1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{
			Files: []*remotefsv1.FileInfo{{Path: "old", Size: 3}},
		}},
	}))

	s.ApplyRename("old", &remotefsv1.FileInfo{Path: "new", Size: 3})

	_, ok := s.Lookup("old")
	assert.False(t, ok)
	info, ok := s.Lookup("new")
	require.True(t, ok)
	assert.EqualValues(t, 3, info.GetSize())
}

func TestSession_Apply_ConcurrentWithChange(t *testing.T) {
	s := NewSession("u1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.ApplyWriteResult(&remotefsv1.FileInfo{
				Path: fmt.Sprintf("p%d", i), Size: int64(i),
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = s.Lookup(fmt.Sprintf("p%d", i))
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestSession_Apply" -count=1 ./pkg/session/...`
Expected: 编译失败

- [ ] **Step 3: 实现 Apply\* 方法**

在 `pkg/session/session.go` 中（`applyChange` 附近）追加：

```go
// ApplyWriteResult merges a FileInfo carried by a write response into the
// session's local tree. Used by view-layer helpers right after a successful
// write to remove the visibility window between the response and the matching
// fsnotify change event.
func (s *Session) ApplyWriteResult(info *remotefsv1.FileInfo) {
	if s == nil || info == nil || info.GetPath() == "" {
		return
	}
	tree := s.currentTree()
	_ = tree.Insert(info)
}

// ApplyDelete removes a path (and any descendants) from the session tree.
func (s *Session) ApplyDelete(relPath string) {
	if s == nil || relPath == "" {
		return
	}
	tree := s.currentTree()
	tree.Delete(relPath)
}

// ApplyRename atomically removes oldRel and inserts newInfo.
func (s *Session) ApplyRename(oldRel string, newInfo *remotefsv1.FileInfo) {
	if s == nil {
		return
	}
	tree := s.currentTree()
	if oldRel != "" {
		tree.Delete(oldRel)
	}
	if newInfo != nil && newInfo.GetPath() != "" {
		_ = tree.Insert(newInfo)
	}
}
```

> `tree.Insert` 与 `tree.Delete` 是 `pkg/fstree.Tree` 已有的并发安全方法（与 `applyChange` 走相同的内部 mutex）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/session/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/session/session.go pkg/session/session_test.go
git commit -m "feat(session): add ApplyWriteResult/ApplyDelete/ApplyRename"
```

---

## Phase E — Server view 层

### Task 14: 错误哨兵 + classifyError 扩展 + classifyRequestErr + withRequestTimeout

**Files:**
- Modify: `pkg/fusefs/view.go`
- Modify: `pkg/fusefs/view_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/fusefs/view_test.go` 末尾追加：

```go
func TestClassifyError_KeywordTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"linux not found", `syncer: read "x": open x: no such file or directory`, ErrPathNotFound},
		{"windows not found", `syncer: read "x": open x: The system cannot find the file specified.`, ErrPathNotFound},
		{"linux denied", `syncer: write "x": open x: permission denied`, ErrPermissionDenied},
		{"windows denied", `syncer: write "x": open x: Access is denied.`, ErrPermissionDenied},
		{"linux exists", `syncer: mkdir "d": mkdir d: file exists`, ErrAlreadyExists},
		{"linux not a dir", `syncer: list "x": readdir x: not a directory`, ErrNotDirectory},
		{"linux is a dir", `syncer: read "d": read d: is a directory`, ErrIsDirectory},
		{"linux not empty", `syncer: delete "d": remove d: directory not empty`, ErrDirectoryNotEmpty},
		{"linux exdev", `syncer: rename "a"->"b": rename a b: invalid cross-device link`, ErrCrossDevice},
		{"linux einval", `syncer: truncate "x": truncate x: invalid argument`, ErrInvalidArgument},
		{"unknown", `syncer: write "x": open x: weird unrecognised`, ErrIOFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyError(tc.in)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestClassifyRequestErr_TimeoutAndCanceled(t *testing.T) {
	assert.ErrorIs(t, classifyRequestErr(context.DeadlineExceeded, ""), ErrRequestTimeout)
	assert.ErrorIs(t, classifyRequestErr(context.Canceled, ""), context.Canceled)
}

func TestClassifyRequestErr_GenericFailure(t *testing.T) {
	err := classifyRequestErr(nil, `syncer: read "x": open x: permission denied`)
	assert.ErrorIs(t, err, ErrPermissionDenied)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestClassifyError_KeywordTable|TestClassifyRequestErr" -count=1 ./pkg/fusefs/...`
Expected: 编译失败

- [ ] **Step 3: 扩充 view.go 哨兵 + classifyError + classifyRequestErr**

在 `pkg/fusefs/view.go` 顶部 var 块新增：

```go
var (
	ErrUnsupportedPlatform = errors.New("fusefs: unsupported platform")
	ErrNilManager          = errors.New("fusefs: nil manager")
	ErrEmptyMountpoint     = errors.New("fusefs: empty mountpoint")
	ErrSessionOffline      = errors.New("fusefs: session offline")
	ErrPathNotFound        = errors.New("fusefs: path not found")
	ErrNotDirectory        = errors.New("fusefs: not a directory")
	ErrIsDirectory         = errors.New("fusefs: is a directory")
	ErrReadFailed          = errors.New("fusefs: read failed")

	ErrPermissionDenied  = errors.New("fusefs: permission denied")
	ErrAlreadyExists     = errors.New("fusefs: already exists")
	ErrDirectoryNotEmpty = errors.New("fusefs: directory not empty")
	ErrCrossDevice       = errors.New("fusefs: cross device")
	ErrInvalidArgument   = errors.New("fusefs: invalid argument")
	ErrIOFailed          = errors.New("fusefs: io failed")
	ErrRequestTimeout    = errors.New("fusefs: request timeout")
	ErrSessionFailed     = errors.New("fusefs: session failed")
)
```

把现有 `classifyReadError` 重命名为 `classifyError` 并扩充：

```go
func classifyError(msg string) error {
	lower := strings.ToLower(msg)
	switch {
	case containsAny(lower, "no such file", "cannot find the file", "cannot find the path"):
		return fmt.Errorf("%w: %s", ErrPathNotFound, msg)
	case containsAny(lower, "permission denied", "access is denied"):
		return fmt.Errorf("%w: %s", ErrPermissionDenied, msg)
	case containsAny(lower, "file exists", "already exists"):
		return fmt.Errorf("%w: %s", ErrAlreadyExists, msg)
	case containsAny(lower, "not a directory"):
		return fmt.Errorf("%w: %s", ErrNotDirectory, msg)
	case containsAny(lower, "is a directory"):
		return fmt.Errorf("%w: %s", ErrIsDirectory, msg)
	case containsAny(lower, "directory not empty"):
		return fmt.Errorf("%w: %s", ErrDirectoryNotEmpty, msg)
	case containsAny(lower, "cross-device", "invalid cross-device"):
		return fmt.Errorf("%w: %s", ErrCrossDevice, msg)
	case containsAny(lower, "invalid argument"):
		return fmt.Errorf("%w: %s", ErrInvalidArgument, msg)
	default:
		return fmt.Errorf("%w: %s", ErrIOFailed, msg)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
```

把所有引用 `classifyReadError` 的位置（特别是 `readChunk`）改为 `classifyError`。

新增 `classifyRequestErr`：

```go
func classifyRequestErr(reqErr error, respErr string) error {
	if reqErr != nil {
		switch {
		case errors.Is(reqErr, context.DeadlineExceeded):
			return fmt.Errorf("%w: %v", ErrRequestTimeout, reqErr)
		case errors.Is(reqErr, context.Canceled):
			return reqErr
		default:
			return fmt.Errorf("%w: %v", ErrSessionFailed, reqErr)
		}
	}
	if respErr == "" {
		return nil
	}
	return classifyError(respErr)
}
```

新增 ctx 复合：

```go
func withRequestTimeout(ctx context.Context, manager *session.Manager) (context.Context, context.CancelFunc) {
	timeout := manager.RequestTimeout()
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -count=1 ./pkg/fusefs/...`
Expected: PASS（含旧的 readChunk 测试）

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/view.go pkg/fusefs/view_test.go
git commit -m "feat(fusefs): expand error sentinels, classifyError, classifyRequestErr"
```

---

### Task 15: writeChunk + createFile + truncatePath helper

**Files:**
- Modify: `pkg/fusefs/view.go`
- Modify: `pkg/fusefs/view_test.go`

- [ ] **Step 1: 写失败的测试**

> 测试需要一个能由测试驱动的 fake daemon。这里复用 Phase 3 已经在 `view_test.go` 里使用的 manager+session 注册模式：注入一个 `MockConnectStream`，启动 `Session.Serve` goroutine，再用一个 fake responder goroutine 拦截请求并按需回应。

在 `pkg/fusefs/view_test.go` 末尾追加（如果 helper 已存在则复用）：

```go
// startFakeSession registers a session and runs a goroutine that handles
// every outbound FileRequest with the supplied responder.
func startFakeSession(t *testing.T, userID string, responder func(*remotefsv1.FileRequest) *remotefsv1.FileResponse) (*session.Manager, func()) {
	t.Helper()

	mgr := session.NewManager(session.ManagerOptions{RequestTimeout: time.Second})
	sess := session.NewSession(userID)
	_, err := mgr.Register(sess)
	require.NoError(t, err)

	stream := testutil.NewMockConnectStream(context.Background())
	require.NoError(t, sess.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	go func() { _ = sess.Serve(context.Background(), stream) }()
	go func() {
		for {
			msg, err := stream.AwaitSend(2 * time.Second)
			if err != nil {
				return
			}
			req := msg.GetRequest()
			if req == nil {
				continue
			}
			resp := responder(req)
			stream.PushRecv(&remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Response{Response: resp},
			})
		}
	}()

	cleanup := func() {
		stream.CloseRecv()
		mgr.Remove(sess)
	}
	return mgr, cleanup
}

func TestWriteChunk_Success(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		w := req.GetWrite()
		require.NotNil(t, w)
		assert.Equal(t, "a.txt", w.GetPath())
		assert.EqualValues(t, 4, w.GetOffset())
		assert.Equal(t, []byte("ab"), w.GetContent())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 6},
			},
		}
	})
	defer cleanup()

	err := writeChunk(context.Background(), mgr, "u1", "a.txt", 4, []byte("ab"))
	require.NoError(t, err)

	sess, _ := mgr.Get("u1")
	info, ok := sess.Lookup("a.txt")
	require.True(t, ok)
	assert.EqualValues(t, 6, info.GetSize())
}

func TestWriteChunk_Failure(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   false,
			Error:     `syncer: write "a.txt": open a.txt: permission denied`,
		}
	})
	defer cleanup()

	err := writeChunk(context.Background(), mgr, "u1", "a.txt", 0, []byte("x"))
	assert.ErrorIs(t, err, ErrPermissionDenied)
}

func TestCreateFile_Success(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		w := req.GetWrite()
		require.NotNil(t, w)
		assert.Empty(t, w.GetContent())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "new.txt", Size: 0},
			},
		}
	})
	defer cleanup()

	err := createFile(context.Background(), mgr, "u1", "new.txt")
	require.NoError(t, err)
	sess, _ := mgr.Get("u1")
	_, ok := sess.Lookup("new.txt")
	assert.True(t, ok)
}

func TestTruncatePath_Success(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		tr := req.GetTruncate()
		require.NotNil(t, tr)
		assert.EqualValues(t, 3, tr.GetSize())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 3},
			},
		}
	})
	defer cleanup()

	err := truncatePath(context.Background(), mgr, "u1", "a.txt", 3)
	require.NoError(t, err)
}
```

新增 import：`"flyingEirc/Rclaude/internal/testutil"`、`"flyingEirc/Rclaude/pkg/session"`（如已 import 则复用）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestWriteChunk|TestCreateFile|TestTruncatePath" -count=1 ./pkg/fusefs/...`
Expected: 编译失败（helper 不存在）

- [ ] **Step 3: 实现三个 helper**

在 `pkg/fusefs/view.go` 末尾追加：

```go
func writeChunk(ctx context.Context, manager *session.Manager, userID, relPath string, offset int64, data []byte) error {
	current, err := requireSession(manager, userID)
	if err != nil {
		return err
	}
	rctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, reqErr := current.Request(rctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{
				Path:    relPath,
				Content: data,
				Offset:  offset,
			},
		},
	})
	if err := classifyRequestErr(reqErr, respErrString(resp)); err != nil {
		return err
	}
	if info := resp.GetInfo(); info != nil {
		current.ApplyWriteResult(info)
	}
	return nil
}

func createFile(ctx context.Context, manager *session.Manager, userID, relPath string) error {
	current, err := requireSession(manager, userID)
	if err != nil {
		return err
	}
	rctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, reqErr := current.Request(rctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{Path: relPath},
		},
	})
	if err := classifyRequestErr(reqErr, respErrString(resp)); err != nil {
		return err
	}
	if info := resp.GetInfo(); info != nil {
		current.ApplyWriteResult(info)
	}
	return nil
}

func truncatePath(ctx context.Context, manager *session.Manager, userID, relPath string, size int64) error {
	current, err := requireSession(manager, userID)
	if err != nil {
		return err
	}
	rctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, reqErr := current.Request(rctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Truncate{
			Truncate: &remotefsv1.TruncateReq{Path: relPath, Size: size},
		},
	})
	if err := classifyRequestErr(reqErr, respErrString(resp)); err != nil {
		return err
	}
	if info := resp.GetInfo(); info != nil {
		current.ApplyWriteResult(info)
	}
	return nil
}

func respErrString(resp *remotefsv1.FileResponse) string {
	if resp == nil || resp.GetSuccess() {
		return ""
	}
	return resp.GetError()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/fusefs/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/view.go pkg/fusefs/view_test.go
git commit -m "feat(fusefs): add writeChunk/createFile/truncatePath view helpers"
```

---

### Task 16: mkdirAt + removePath + renamePath helper

**Files:**
- Modify: `pkg/fusefs/view.go`
- Modify: `pkg/fusefs/view_test.go`

- [ ] **Step 1: 写失败的测试**

在 `view_test.go` 末尾追加：

```go
func TestMkdirAt_Recursive(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		mk := req.GetMkdir()
		require.NotNil(t, mk)
		assert.True(t, mk.GetRecursive())
		assert.Equal(t, "a/b", mk.GetPath())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a/b", IsDir: true},
			},
		}
	})
	defer cleanup()
	require.NoError(t, mkdirAt(context.Background(), mgr, "u1", "a/b", true))
}

func TestRemovePath_Success(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		require.NotNil(t, req.GetDelete())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
		}
	})
	defer cleanup()
	require.NoError(t, removePath(context.Background(), mgr, "u1", "x"))
}

func TestRemovePath_RemovesFromSession(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		return &remotefsv1.FileResponse{RequestId: req.GetRequestId(), Success: true}
	})
	defer cleanup()

	sess, _ := mgr.Get("u1")
	sess.ApplyWriteResult(&remotefsv1.FileInfo{Path: "x", Size: 1})

	require.NoError(t, removePath(context.Background(), mgr, "u1", "x"))

	_, ok := sess.Lookup("x")
	assert.False(t, ok)
}

func TestRenamePath_AppliesNewInfo(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		rn := req.GetRename()
		require.NotNil(t, rn)
		assert.Equal(t, "a", rn.GetOldPath())
		assert.Equal(t, "b", rn.GetNewPath())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "b", Size: 4},
			},
		}
	})
	defer cleanup()

	sess, _ := mgr.Get("u1")
	sess.ApplyWriteResult(&remotefsv1.FileInfo{Path: "a", Size: 4})

	require.NoError(t, renamePath(context.Background(), mgr, "u1", "a", "b"))

	_, ok := sess.Lookup("a")
	assert.False(t, ok)
	info, ok := sess.Lookup("b")
	require.True(t, ok)
	assert.EqualValues(t, 4, info.GetSize())
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestMkdirAt|TestRemovePath|TestRenamePath" -count=1 ./pkg/fusefs/...`
Expected: 编译失败

- [ ] **Step 3: 实现三个 helper**

在 `pkg/fusefs/view.go` 末尾追加：

```go
func mkdirAt(ctx context.Context, manager *session.Manager, userID, relPath string, recursive bool) error {
	current, err := requireSession(manager, userID)
	if err != nil {
		return err
	}
	rctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, reqErr := current.Request(rctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Mkdir{
			Mkdir: &remotefsv1.MkdirReq{Path: relPath, Recursive: recursive},
		},
	})
	if err := classifyRequestErr(reqErr, respErrString(resp)); err != nil {
		return err
	}
	if info := resp.GetInfo(); info != nil {
		current.ApplyWriteResult(info)
	}
	return nil
}

func removePath(ctx context.Context, manager *session.Manager, userID, relPath string) error {
	current, err := requireSession(manager, userID)
	if err != nil {
		return err
	}
	rctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, reqErr := current.Request(rctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Delete{
			Delete: &remotefsv1.DeleteReq{Path: relPath},
		},
	})
	if err := classifyRequestErr(reqErr, respErrString(resp)); err != nil {
		return err
	}
	current.ApplyDelete(relPath)
	return nil
}

func renamePath(ctx context.Context, manager *session.Manager, userID, oldRel, newRel string) error {
	current, err := requireSession(manager, userID)
	if err != nil {
		return err
	}
	rctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, reqErr := current.Request(rctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Rename{
			Rename: &remotefsv1.RenameReq{OldPath: oldRel, NewPath: newRel},
		},
	})
	if err := classifyRequestErr(reqErr, respErrString(resp)); err != nil {
		return err
	}
	current.ApplyRename(oldRel, resp.GetInfo())
	return nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/fusefs/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/view.go pkg/fusefs/view_test.go
git commit -m "feat(fusefs): add mkdirAt/removePath/renamePath view helpers"
```

---

## Phase F — FUSE Linux Node 接口

### Task 17: errnoFromError 扩充 + Open 接受写 flag

**Files:**
- Modify: `pkg/fusefs/mount_linux.go`
- Modify: `pkg/fusefs/mount_linux_test.go`（如不存在则创建）

- [ ] **Step 1: 写失败的测试**

在 `pkg/fusefs/mount_linux_test.go` 末尾追加（保留 build tag `//go:build linux`，文件如不存在新建）：

```go
//go:build linux

package fusefs

import (
	"context"
	"errors"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrnoFromError_NewSentinels(t *testing.T) {
	cases := []struct {
		err  error
		want syscall.Errno
	}{
		{ErrPermissionDenied, syscall.EACCES},
		{ErrAlreadyExists, syscall.EEXIST},
		{ErrDirectoryNotEmpty, syscall.ENOTEMPTY},
		{ErrCrossDevice, syscall.EXDEV},
		{ErrInvalidArgument, syscall.EINVAL},
		{ErrRequestTimeout, syscall.ETIMEDOUT},
		{ErrSessionFailed, syscall.EIO},
		{context.Canceled, syscall.EINTR},
		{errors.New("totally unknown"), syscall.EIO},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, errnoFromError(tc.err))
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run TestErrnoFromError_NewSentinels -count=1 ./pkg/fusefs/...`
Expected: 失败（旧 errnoFromError 没覆盖新哨兵）

- [ ] **Step 3: 扩充 errnoFromError + 改 Open**

修改 `pkg/fusefs/mount_linux.go` 的 `errnoFromError`：

```go
func errnoFromError(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	case errors.Is(err, ErrRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return syscall.ETIMEDOUT
	case errors.Is(err, ErrSessionOffline), errors.Is(err, ErrSessionFailed):
		return syscall.EIO
	case errors.Is(err, ErrPathNotFound):
		return syscall.ENOENT
	case errors.Is(err, ErrPermissionDenied):
		return syscall.EACCES
	case errors.Is(err, ErrAlreadyExists):
		return syscall.EEXIST
	case errors.Is(err, ErrNotDirectory):
		return syscall.ENOTDIR
	case errors.Is(err, ErrIsDirectory):
		return syscall.EISDIR
	case errors.Is(err, ErrDirectoryNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, ErrCrossDevice):
		return syscall.EXDEV
	case errors.Is(err, ErrInvalidArgument):
		return syscall.EINVAL
	default:
		return syscall.EIO
	}
}
```

修改 `workspaceNode.Open`，去掉 `EROFS`：

```go
func (n *workspaceNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	info, err := lookupInfo(n.manager, n.userID, n.relPath)
	if err != nil {
		return nil, 0, errnoFromError(err)
	}
	if info.GetIsDir() {
		return nil, 0, syscall.EISDIR
	}
	return nil, 0, 0
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `GOOS=linux go test -count=1 ./pkg/fusefs/...`（在 Windows 上由于 build tag，本测试不会跑；在 Linux 或 WSL 上跑）
Expected: PASS。在非 Linux 环境本任务在 CI 由 Phase G 的最终验收一起兜底。

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/mount_linux.go pkg/fusefs/mount_linux_test.go
git commit -m "feat(fusefs): expand errnoFromError mapping; allow O_WRONLY/O_RDWR"
```

---

### Task 18: workspaceNode.Write + Create

**Files:**
- Modify: `pkg/fusefs/mount_linux.go`
- Modify: `pkg/fusefs/mount_linux_test.go`

- [ ] **Step 1: 写失败的测试**

在 `mount_linux_test.go` 末尾追加：

```go
func TestWorkspaceNode_Write_OffsetForwarded(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		w := req.GetWrite()
		require.NotNil(t, w)
		assert.EqualValues(t, 42, w.GetOffset())
		assert.Equal(t, []byte("hi"), w.GetContent())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(), Success: true,
			Result: &remotefsv1.FileResponse_Info{Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 44}},
		}
	})
	defer cleanup()
	sess, _ := mgr.Get("u1")
	sess.ApplyWriteResult(&remotefsv1.FileInfo{Path: "a.txt", Size: 44})

	node := &workspaceNode{manager: mgr, userID: "u1", relPath: "a.txt"}
	written, errno := node.Write(context.Background(), nil, []byte("hi"), 42)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.EqualValues(t, 2, written)
}

func TestWorkspaceNode_Create_Success(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(), Success: true,
			Result: &remotefsv1.FileResponse_Info{Info: &remotefsv1.FileInfo{Path: "new.txt", Size: 0}},
		}
	})
	defer cleanup()

	parent := &workspaceNode{manager: mgr, userID: "u1", relPath: ""}
	out := &fuse.EntryOut{}
	inode, _, _, errno := parent.Create(context.Background(), "new.txt", uint32(syscall.O_WRONLY|syscall.O_CREAT), 0o644, out)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.NotNil(t, inode)
}

func TestWorkspaceNode_Create_AlreadyExists(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   false,
			Error:     `syncer: write "x.txt": open x.txt: file exists`,
		}
	})
	defer cleanup()

	parent := &workspaceNode{manager: mgr, userID: "u1", relPath: ""}
	out := &fuse.EntryOut{}
	_, _, _, errno := parent.Create(context.Background(), "x.txt", 0, 0o644, out)
	assert.Equal(t, syscall.EEXIST, errno)
}
```

> 把 `startFakeSession` 提取到 `pkg/fusefs/test_helpers_test.go`（共享给 view_test 与 mount_linux_test）；如果 view_test 中已有同名 helper，则把它移到一个 `_test.go`，所有测试共享。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestWorkspaceNode_Write|TestWorkspaceNode_Create" -count=1 ./pkg/fusefs/...`
Expected: 失败

- [ ] **Step 3: 实现 Write + Create**

在 `mount_linux.go` 的 `var ( _ = ... )` 块加：

```go
_ = (fs.NodeWriter)((*workspaceNode)(nil))
_ = (fs.NodeCreater)((*workspaceNode)(nil))
```

新增方法：

```go
func (n *workspaceNode) Write(ctx context.Context, _ fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if err := writeChunk(ctx, n.manager, n.userID, n.relPath, off, data); err != nil {
		return 0, errnoFromError(err)
	}
	return uint32(len(data)), 0
}

func (n *workspaceNode) Create(ctx context.Context, name string, _ uint32, _ uint32, out *fuse.EntryOut) (
	*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	childRel := childPath(n.relPath, name)
	if err := createFile(ctx, n.manager, n.userID, childRel); err != nil {
		return nil, nil, 0, errnoFromError(err)
	}
	info, err := lookupInfo(n.manager, n.userID, childRel)
	if err != nil {
		return nil, nil, 0, errnoFromError(err)
	}
	fillEntryOut(out, info)
	child := &workspaceNode{manager: n.manager, userID: n.userID, relPath: childRel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)})
	return inode, nil, 0, 0
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/fusefs/...`（Linux/WSL；Windows 跳过 build-tagged 文件，由 view_test 兜底）
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/mount_linux.go pkg/fusefs/mount_linux_test.go
git commit -m "feat(fusefs): implement Write and Create on workspaceNode"
```

---

### Task 19: workspaceNode.Mkdir + Unlink + Rmdir

**Files:**
- Modify: `pkg/fusefs/mount_linux.go`
- Modify: `pkg/fusefs/mount_linux_test.go`

- [ ] **Step 1: 写失败的测试**

在 `mount_linux_test.go` 追加：

```go
func TestWorkspaceNode_Mkdir(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		mk := req.GetMkdir()
		require.NotNil(t, mk)
		assert.Equal(t, "d", mk.GetPath())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(), Success: true,
			Result: &remotefsv1.FileResponse_Info{Info: &remotefsv1.FileInfo{Path: "d", IsDir: true}},
		}
	})
	defer cleanup()

	parent := &workspaceNode{manager: mgr, userID: "u1", relPath: ""}
	out := &fuse.EntryOut{}
	inode, errno := parent.Mkdir(context.Background(), "d", 0o755, out)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.NotNil(t, inode)
}

func TestWorkspaceNode_Unlink(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		require.NotNil(t, req.GetDelete())
		return &remotefsv1.FileResponse{RequestId: req.GetRequestId(), Success: true}
	})
	defer cleanup()

	parent := &workspaceNode{manager: mgr, userID: "u1", relPath: ""}
	errno := parent.Unlink(context.Background(), "x.txt")
	assert.Equal(t, syscall.Errno(0), errno)
}

func TestWorkspaceNode_Rmdir(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		require.NotNil(t, req.GetDelete())
		return &remotefsv1.FileResponse{RequestId: req.GetRequestId(), Success: true}
	})
	defer cleanup()

	parent := &workspaceNode{manager: mgr, userID: "u1", relPath: ""}
	errno := parent.Rmdir(context.Background(), "d")
	assert.Equal(t, syscall.Errno(0), errno)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestWorkspaceNode_Mkdir|TestWorkspaceNode_Unlink|TestWorkspaceNode_Rmdir" -count=1 ./pkg/fusefs/...`
Expected: 失败

- [ ] **Step 3: 实现三个方法**

在 `mount_linux.go` 的接口断言块加：

```go
_ = (fs.NodeMkdirer)((*workspaceNode)(nil))
_ = (fs.NodeUnlinker)((*workspaceNode)(nil))
_ = (fs.NodeRmdirer)((*workspaceNode)(nil))
```

```go
func (n *workspaceNode) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childRel := childPath(n.relPath, name)
	if err := mkdirAt(ctx, n.manager, n.userID, childRel, false); err != nil {
		return nil, errnoFromError(err)
	}
	info, err := lookupInfo(n.manager, n.userID, childRel)
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillEntryOut(out, info)
	child := &workspaceNode{manager: n.manager, userID: n.userID, relPath: childRel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)}), 0
}

func (n *workspaceNode) Unlink(ctx context.Context, name string) syscall.Errno {
	childRel := childPath(n.relPath, name)
	if err := removePath(ctx, n.manager, n.userID, childRel); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (n *workspaceNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	childRel := childPath(n.relPath, name)
	if err := removePath(ctx, n.manager, n.userID, childRel); err != nil {
		return errnoFromError(err)
	}
	return 0
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/fusefs/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/mount_linux.go pkg/fusefs/mount_linux_test.go
git commit -m "feat(fusefs): implement Mkdir/Unlink/Rmdir on workspaceNode"
```

---

### Task 20: workspaceNode.Rename + Setattr

**Files:**
- Modify: `pkg/fusefs/mount_linux.go`
- Modify: `pkg/fusefs/mount_linux_test.go`

- [ ] **Step 1: 写失败的测试**

```go
func TestWorkspaceNode_Rename_FullPath(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		rn := req.GetRename()
		require.NotNil(t, rn)
		assert.Equal(t, "a/x", rn.GetOldPath())
		assert.Equal(t, "b/x", rn.GetNewPath())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(), Success: true,
			Result: &remotefsv1.FileResponse_Info{Info: &remotefsv1.FileInfo{Path: "b/x"}},
		}
	})
	defer cleanup()

	src := &workspaceNode{manager: mgr, userID: "u1", relPath: "a"}
	dst := &workspaceNode{manager: mgr, userID: "u1", relPath: "b"}
	errno := src.Rename(context.Background(), "x", dst, "x", 0)
	assert.Equal(t, syscall.Errno(0), errno)
}

func TestWorkspaceNode_Setattr_OnlySize(t *testing.T) {
	var truncateSeen bool
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		if tr := req.GetTruncate(); tr != nil {
			truncateSeen = true
			assert.EqualValues(t, 7, tr.GetSize())
			return &remotefsv1.FileResponse{
				RequestId: req.GetRequestId(), Success: true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 7},
				},
			}
		}
		t.Fatalf("unexpected req: %T", req.GetOperation())
		return nil
	})
	defer cleanup()

	sess, _ := mgr.Get("u1")
	sess.ApplyWriteResult(&remotefsv1.FileInfo{Path: "a.txt", Size: 10})

	node := &workspaceNode{manager: mgr, userID: "u1", relPath: "a.txt"}
	in := &fuse.SetAttrIn{}
	in.Size = 7
	in.Valid = fuse.FATTR_SIZE
	out := &fuse.AttrOut{}
	errno := node.Setattr(context.Background(), nil, in, out)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.True(t, truncateSeen)
}

func TestWorkspaceNode_Setattr_NoSizeIsNoop(t *testing.T) {
	mgr, cleanup := startFakeSession(t, "u1", func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		t.Fatalf("no RPC expected; got %T", req.GetOperation())
		return nil
	})
	defer cleanup()

	sess, _ := mgr.Get("u1")
	sess.ApplyWriteResult(&remotefsv1.FileInfo{Path: "a.txt", Size: 10})

	node := &workspaceNode{manager: mgr, userID: "u1", relPath: "a.txt"}
	in := &fuse.SetAttrIn{}
	in.Mode = 0o600
	in.Valid = fuse.FATTR_MODE
	out := &fuse.AttrOut{}
	errno := node.Setattr(context.Background(), nil, in, out)
	assert.Equal(t, syscall.Errno(0), errno)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test -run "TestWorkspaceNode_Rename|TestWorkspaceNode_Setattr" -count=1 ./pkg/fusefs/...`
Expected: 失败

- [ ] **Step 3: 实现 Rename + Setattr**

在 `mount_linux.go` 接口断言块加：

```go
_ = (fs.NodeRenamer)((*workspaceNode)(nil))
_ = (fs.NodeSetattrer)((*workspaceNode)(nil))
```

```go
func (n *workspaceNode) Rename(ctx context.Context, oldName string, newParent fs.InodeEmbedder, newName string, _ uint32) syscall.Errno {
	target, ok := newParent.(*workspaceNode)
	if !ok {
		return syscall.EINVAL
	}
	if target.userID != n.userID {
		return syscall.EXDEV
	}
	oldRel := childPath(n.relPath, oldName)
	newRel := childPath(target.relPath, newName)
	if err := renamePath(ctx, n.manager, n.userID, oldRel, newRel); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (n *workspaceNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if in.Valid&fuse.FATTR_SIZE != 0 {
		if err := truncatePath(ctx, n.manager, n.userID, n.relPath, int64(in.Size)); err != nil {
			return errnoFromError(err)
		}
	}
	return n.Getattr(ctx, fh, out)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test -race -count=1 ./pkg/fusefs/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/fusefs/mount_linux.go pkg/fusefs/mount_linux_test.go
git commit -m "feat(fusefs): implement Rename and Setattr (size only)"
```

---

## Phase G — 端到端、装配、验收

### Task 21: internal/testutil/inmem_transport.go

**Files:**
- Create: `internal/testutil/inmem_transport.go`

- [ ] **Step 1: 写文件**

```go
package testutil

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/session"
	"flyingEirc/Rclaude/pkg/syncer"
)

// InmemPair connects one server-side Session to a daemon-side syncer.Handle
// that operates on a real temp directory, all in process. It is intended for
// integration tests that exercise the full write pipeline without spinning
// up a gRPC server or mounting FUSE.
type InmemPair struct {
	Manager   *session.Manager
	UserID    string
	DaemonDir string
	Cleanup   func()
}

// StartInmemPair wires a fresh manager + session + responder loop together
// against the supplied (or auto-allocated) daemon root.
func StartInmemPair(t *testing.T, daemonRoot string) *InmemPair {
	t.Helper()
	if daemonRoot == "" {
		daemonRoot = t.TempDir()
	} else {
		require.NoError(t, ensureDir(daemonRoot))
	}

	mgr := session.NewManager(session.ManagerOptions{RequestTimeout: 5 * time.Second})
	userID := "u-test"
	sess := session.NewSession(userID)
	if _, err := mgr.Register(sess); err != nil {
		t.Fatalf("register session: %v", err)
	}
	require.NoError(t, sess.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	stream := NewMockConnectStream(context.Background())
	go func() { _ = sess.Serve(context.Background(), stream) }()

	handleOpts := syncer.HandleOptions{
		Root: daemonRoot,
		// locker / selfWrites both nil → safeAcquire still functions; concurrency
		// guarantees not under test here.
	}

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			msg, err := stream.AwaitSend(time.Second)
			if err != nil {
				continue
			}
			req := msg.GetRequest()
			if req == nil {
				continue
			}
			resp := syncer.Handle(req, handleOpts)
			stream.PushRecv(&remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Response{Response: resp},
			})
		}
	}()

	cleanup := func() {
		close(stop)
		stream.CloseRecv()
		mgr.Remove(sess)
	}

	return &InmemPair{
		Manager:   mgr,
		UserID:    userID,
		DaemonDir: daemonRoot,
		Cleanup:   cleanup,
	}
}

// AbsPath returns the absolute path on the daemon side for a relative path.
func (p *InmemPair) AbsPath(rel string) string {
	return filepath.Join(p.DaemonDir, rel)
}

func ensureDir(dir string) error {
	return nil // 调用方传 t.TempDir 时已存在；其它情形交给上层验证
}
```

- [ ] **Step 2: 验证编译**

Run: `go build ./internal/testutil/...`
Expected: 无错误

- [ ] **Step 3: 提交**

```bash
git add internal/testutil/inmem_transport.go
git commit -m "feat(testutil): add InmemPair for end-to-end write tests"
```

---

### Task 22: pkg/fusefs/inmem_e2e_test.go（7 个端到端用例）

**Files:**
- Create: `pkg/fusefs/inmem_e2e_test.go`

- [ ] **Step 1: 写测试**

```go
package fusefs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
)

func TestInmem_CreateAndWrite(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, []byte("hello")))

	got, err := os.ReadFile(pair.AbsPath("a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestInmem_Mkdir_Unlink_Rmdir(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	require.NoError(t, mkdirAt(context.Background(), pair.Manager, pair.UserID, "d", false))
	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "d/x"))
	require.NoError(t, removePath(context.Background(), pair.Manager, pair.UserID, "d/x"))
	require.NoError(t, removePath(context.Background(), pair.Manager, pair.UserID, "d"))

	_, err := os.Stat(pair.AbsPath("d"))
	assert.True(t, os.IsNotExist(err))
}

func TestInmem_Truncate(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, []byte("hello world")))
	require.NoError(t, truncatePath(context.Background(), pair.Manager, pair.UserID, "a.txt", 3))

	got, err := os.ReadFile(pair.AbsPath("a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hel", string(got))
}

func TestInmem_Rename_AcrossDir(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	require.NoError(t, mkdirAt(context.Background(), pair.Manager, pair.UserID, "a", false))
	require.NoError(t, mkdirAt(context.Background(), pair.Manager, pair.UserID, "b", false))
	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a/x"))
	require.NoError(t, renamePath(context.Background(), pair.Manager, pair.UserID, "a/x", "b/x"))

	_, err := os.Stat(pair.AbsPath("a/x"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(pair.AbsPath("b/x"))
	assert.NoError(t, err)
}

func TestInmem_WriteFailure_NotFound(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	err := truncatePath(context.Background(), pair.Manager, pair.UserID, "missing", 0)
	assert.ErrorIs(t, err, ErrPathNotFound)
}

func TestInmem_ApplyWriteResult_Visible(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))

	sess, _ := pair.Manager.Get(pair.UserID)
	info, ok := sess.Lookup("a.txt")
	require.True(t, ok, "newly created file must be visible immediately via ApplyWriteResult")
	assert.Equal(t, "a.txt", info.GetPath())
}

func TestInmem_PathLocker_NoDeadlockOnRename(t *testing.T) {
	pair := testutil.StartInmemPair(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "x"))
	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "y"))

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				_ = renamePath(context.Background(), pair.Manager, pair.UserID, "x", "y")
			}()
			go func() {
				defer wg.Done()
				_ = renamePath(context.Background(), pair.Manager, pair.UserID, "y", "x")
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("rename pair deadlocked under in-mem transport")
	}

	_ = filepath.Join // 防 unused import 警告
	_ = remotefsv1.ChangeType_CHANGE_TYPE_UNSPECIFIED
}
```

- [ ] **Step 2: 运行测试**

Run: `go test -race -count=1 -run TestInmem ./pkg/fusefs/...`
Expected: PASS

- [ ] **Step 3: 提交**

```bash
git add pkg/fusefs/inmem_e2e_test.go
git commit -m "test(fusefs): add in-memory end-to-end write pipeline tests"
```

---

### Task 23: app/server/main.go 调用 NewManager(opts)

**Files:**
- Modify: `app/server/main.go`

- [ ] **Step 1: 编辑**

把现有 `manager := session.NewManager()` 改为：

```go
manager := session.NewManager(session.ManagerOptions{
	RequestTimeout: cfg.RequestTimeout,
})
```

确认 `cfg` 已经是 `*config.ServerConfig`（经过 Task 10 的 default 注入后 RequestTimeout 不会为 0）。

- [ ] **Step 2: 验证编译 + 跨平台编译**

Run: `go build ./app/server`
Run: `GOOS=linux GOARCH=amd64 go build ./app/server`
Expected: 都通过

- [ ] **Step 3: 提交**

```bash
git add app/server/main.go
git commit -m "feat(server): pass RequestTimeout via ManagerOptions"
```

---

### Task 24: 阶段最终验收

**Files:** （无代码改动；只跑命令并把结果落到 `开发流程.md`）

- [ ] **Step 1: 格式化**

Run:
```bash
gofumpt -w .
gci write --section standard --section default --section "prefix(flyingEirc/Rclaude)" .
```
Expected: 无 diff 噪音；如果有，按结果决定补 commit。

- [ ] **Step 2: 静态检查**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 3: 依赖收口**

Run: `go mod tidy`
Expected: `go.mod` / `go.sum` 无意外变化

- [ ] **Step 4: 全量带 race 测试**

Run: `go test -race -count=1 -timeout 180s ./...`
Expected: 全部 PASS

- [ ] **Step 5: 构建**

Run: `go build ./...`
Expected: 无错误

- [ ] **Step 6: Linux 交叉编译**

Run: `GOOS=linux GOARCH=amd64 go build ./app/server`
Expected: 无错误

- [ ] **Step 7: 把命令结果摘要写入 `docs/exec-plan/active/202604071253-phase4a-write-ops/开发流程.md`**

按 Phase 3 的格式记录每条命令的退出码与关键输出片段。

- [ ] **Step 8: Linux 真机手动验收（用户在自己的 Linux 环境跑一次）**

```bash
# 一个 shell：启动 daemon
./daemon --config daemon.yaml

# 另一个 shell：启动 server（确保 mountpoint 已创建）
./server --config server.yaml

# 第三个 shell：在挂载点上验证写操作
mountpoint=/path/to/mountpoint/u1
echo hi > $mountpoint/foo.txt
cat $mountpoint/foo.txt          # → hi
mkdir $mountpoint/d
mv $mountpoint/foo.txt $mountpoint/d/bar.txt
cat $mountpoint/d/bar.txt        # → hi
truncate -s 1 $mountpoint/d/bar.txt
cat $mountpoint/d/bar.txt        # → h
rm $mountpoint/d/bar.txt
rmdir $mountpoint/d
```

把每个命令的实际结果摘要落到 `开发流程.md`。任何失败都必须先填进 `测试错误.md` 然后修复并复测。

- [ ] **Step 9: 把阶段从 active 迁到 completed 并写完成摘要**

```bash
mv docs/exec-plan/active/202604071253-phase4a-write-ops docs/exec-plan/completed/202604071253-phase4a-write-ops
```

在 `docs/exec-plan/completed/202604071253-phase4a-write-ops/` 内创建同名 `.md` 完成摘要：

`docs/exec-plan/completed/202604071253-phase4a-write-ops/202604071253-phase4a-write-ops.md`，内容包含：

```markdown
# 202604071253-phase4a-write-ops

## 完成状态
- done

## 验收结果
- gofumpt / gci.exe / golangci-lint / go mod tidy / go test -race / go build / Linux 交叉编译：均通过（命令输出见 开发流程.md）
- Linux 真机 FUSE 挂载验收（Write/Create/Mkdir/Rename/Truncate/Unlink/Rmdir）：全部命令符合预期

## 与 plan 的偏离
（按实际情况记录；无偏离则写 "无"）

## 遗留问题
- 缓存 / 写后内容缓存失效仍属于 Phase 4b 范围
- Setattr 的 mode/atime/uid/gid 在本阶段被静默吞，留给 Phase 6
```

- [ ] **Step 10: 提交阶段归档**

```bash
git add docs/exec-plan/active docs/exec-plan/completed
git commit -m "chore(exec-plan): archive phase 4a (write-ops) to completed"
```

---

## Self-Review（plan 自审）

1. **Spec 覆盖检查**

| spec 条目 | 对应 task |
|---|---|
| §2 proto: WriteFileReq.offset + TruncateReq | T1 |
| §3 M1 pathLocker | T2 |
| §3 M2 selfWriteFilter | T3 |
| §3 M3 handle_write.go 5 函数 | T4-T7 |
| §3 M4 handle.go dispatch | T5-T8 |
| §3 M5 watch.go 抑制 | T9 |
| §3 M6 daemon.go 装配 | T11 |
| §3 M7 config 增量 | T10 |
| §3 M8 ManagerOptions / RequestTimeout | T12 |
| §3 M8 Session.Apply\* | T13 |
| §3 M9 view.go 错误哨兵 + classifyError + classifyRequestErr + withRequestTimeout | T14 |
| §3 M9 6 个 view helper | T15-T16 |
| §3 M10 mount_linux.go Node 接口 + Open + errnoFromError | T17-T20 |
| §3 M11 InmemPair | T21 |
| §3 M12 main.go 装配 | T23 |
| §4.1 daemon error 字符串格式 | T4 helper |
| §4.2 关键字表 + 超时分流 | T14 |
| §4.3 一致性窗口修法 A（写响应回填 + Apply\*） | T5/T6/T7 daemon 端 + T13 server 端 + T15/T16 view 端 |
| §5 测试矩阵 | T2/T3/T5/T6/T7/T9/T12/T13/T14/T15/T16/T17/T18/T19/T20/T22 |
| §5.3 验收命令 | T24 |

无遗漏。

2. **占位符扫描**：plan 中无 TBD/TODO/FIXME；每个 step 都有具体代码或具体命令。

3. **类型一致性**：
   - `pathLocker` / `selfWriteFilter` 命名贯穿 T2-T11 一致
   - `HandleOptions.Locker / SelfWrites` 命名贯穿 T4-T11 一致
   - `WatchOptions.SelfWrites` T9-T11 一致
   - `ManagerOptions.RequestTimeout` T12-T23 一致
   - `Session.ApplyWriteResult / ApplyDelete / ApplyRename` T13-T16 一致
   - 6 个 view helper 命名 `writeChunk / createFile / mkdirAt / removePath / renamePath / truncatePath` T15-T16-T18-T20 一致
   - `errnoFromError` 大小写一致

---

## Todo 列表（与 Task 1:1 对应）

| Task | 主题 | 状态 |
|---|---|---|
| T1 | proto 改动并重新生成 | [x] |
| T2 | pathLocker | [x] |
| T3 | selfWriteFilter | [x] |
| T4 | handle_write.go 公共框架 | [x] |
| T5 | handleWrite | [x] |
| T6 | handleMkdir / handleDelete / handleTruncate | [x] |
| T7 | handleRename（双锁） | [x] |
| T8 | dispatch 清理与全通 | [x] |
| T9 | watch.go 抑制 | [x] |
| T10 | config RequestTimeout / SelfWriteTTL | [x] |
| T11 | daemon.go 装配 | [x] |
| T12 | ManagerOptions + RequestTimeout | [x] |
| T13 | Session.Apply\* | [x] |
| T14 | view 错误哨兵 / classifyError / classifyRequestErr / withRequestTimeout | [x] |
| T15 | writeChunk + createFile + truncatePath | [x] |
| T16 | mkdirAt + removePath + renamePath | [x] |
| T17 | errnoFromError 扩充 + Open 改造 | [x] |
| T18 | workspaceNode.Write + Create | [x] |
| T19 | workspaceNode.Mkdir + Unlink + Rmdir | [x] |
| T20 | workspaceNode.Rename + Setattr | [x] |
| T21 | internal/testutil/inmem_transport.go | [x] |
| T22 | pkg/fusefs/inmem_e2e_test.go | [x] |
| T23 | app/server/main.go NewManager 调整 | [x] |
| T24 | 阶段最终验收 + 归档 | [x] |

依赖图：

```
T1 ──► T4 ──► T5 ──► T6 ──► T7 ──► T8
T2 ──┘         │      │      │
T3 ──► T9      │      │      │
T10 ──► T11 ◄──┤      │      │
T12 ──► T13 ──► T14 ──► T15 ──► T16
                              ╰────► T17 ──► T18 ──► T19 ──► T20
T21 ◄────────────── T13, T15
T21 ──► T22
T23 ◄── T12
T8/T11/T20/T22/T23 ──► T24
```
