package inmemtest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/session"
	"flyingEirc/Rclaude/pkg/syncer"
)

const (
	defaultRequestTimeout = 5 * time.Second
	defaultCacheMaxBytes  = 1 << 20
)

type HarnessOptions struct {
	RequestTimeout     time.Duration
	CacheMaxBytes      int64
	OfflineReadOnlyTTL time.Duration
}

type UserOptions struct {
	UserID      string
	DaemonRoot  string
	MaxReadSize int64
	Faults      FaultHooks
}

type FaultHooks struct {
	BeforeHandle func(*remotefsv1.FileRequest) Action
	AfterHandle  func(*remotefsv1.FileRequest, *remotefsv1.FileResponse) Action
}

type RequestSnapshot struct {
	Kind string
	Path string
}

type Action struct {
	kind     actionKind
	delay    time.Duration
	response *remotefsv1.FileResponse
}

type actionKind uint8

const (
	actionPass actionKind = iota
	actionDelay
	actionDrop
	actionRespond
)

type Harness struct {
	t       testing.TB
	Manager *session.Manager

	mu    sync.Mutex
	users map[string]*UserHandle
	once  sync.Once
}

type UserHandle struct {
	harness *Harness

	Manager   *session.Manager
	Session   *session.Session
	UserID    string
	DaemonDir string

	stream *testutil.MockConnectStream
	cancel context.CancelFunc

	serveErrCh <-chan error
	loopDoneCh <-chan struct{}

	readCount atomic.Int64

	requestMu   sync.Mutex
	lastRequest RequestSnapshot

	faultMu sync.RWMutex
	faults  FaultHooks

	cleanupOnce sync.Once
}

func Pass() Action {
	return Action{kind: actionPass}
}

func Delay(delay time.Duration) Action {
	return Action{kind: actionDelay, delay: delay}
}

func DropConnection(err error) Action {
	_ = err
	return Action{kind: actionDrop}
}

func Respond(resp *remotefsv1.FileResponse) Action {
	return Action{kind: actionRespond, response: resp}
}

func NewHarness(t testing.TB, opts ...HarnessOptions) *Harness {
	t.Helper()

	cfg := HarnessOptions{
		RequestTimeout: defaultRequestTimeout,
		CacheMaxBytes:  defaultCacheMaxBytes,
	}
	if len(opts) > 0 {
		cfg = opts[0]
		if cfg.RequestTimeout <= 0 {
			cfg.RequestTimeout = defaultRequestTimeout
		}
	}

	return &Harness{
		t: t,
		Manager: session.NewManager(session.ManagerOptions{
			RequestTimeout:     cfg.RequestTimeout,
			CacheMaxBytes:      cfg.CacheMaxBytes,
			OfflineReadOnlyTTL: cfg.OfflineReadOnlyTTL,
		}),
		users: make(map[string]*UserHandle),
	}
}

func (h *Harness) AddUser(opts UserOptions) *UserHandle {
	h.t.Helper()

	require.NotEmpty(h.t, opts.UserID)

	h.mu.Lock()
	if _, exists := h.users[opts.UserID]; exists {
		h.mu.Unlock()
		h.t.Fatalf("inmemtest: duplicate user id %q", opts.UserID)
	}
	h.mu.Unlock()

	daemonRoot := ensureDaemonRoot(h.t, opts.DaemonRoot)
	current := h.Manager.NewSession(opts.UserID)
	_, err := h.Manager.Register(current)
	require.NoError(h.t, err)
	require.NoError(h.t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	stream := testutil.NewMockConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		err := current.Serve(ctx, stream)
		h.Manager.HandleDisconnect(current, err)
		errCh <- err
	}()

	user := &UserHandle{
		harness:    h,
		Manager:    h.Manager,
		Session:    current,
		UserID:     opts.UserID,
		DaemonDir:  daemonRoot,
		stream:     stream,
		cancel:     cancel,
		serveErrCh: errCh,
		faults:     opts.Faults,
	}
	user.loopDoneCh = startHandleLoop(ctx, user, syncer.HandleOptions{
		Root:        daemonRoot,
		MaxReadSize: opts.MaxReadSize,
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	h.users[opts.UserID] = user

	return user
}

func (h *Harness) User(userID string) (*UserHandle, bool) {
	if h == nil {
		return nil, false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	user, ok := h.users[userID]
	return user, ok
}

func (h *Harness) Cleanup() {
	if h == nil {
		return
	}

	h.once.Do(func() {
		for _, user := range h.userHandles() {
			user.Cleanup()
		}
	})
}

func (h *Harness) userHandles() []*UserHandle {
	h.mu.Lock()
	defer h.mu.Unlock()

	keys := make([]string, 0, len(h.users))
	for userID := range h.users {
		keys = append(keys, userID)
	}
	sort.Strings(keys)

	out := make([]*UserHandle, 0, len(keys))
	for _, userID := range keys {
		out = append(out, h.users[userID])
	}
	return out
}

func (h *Harness) removeUser(userID string) {
	if h == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.users, userID)
}

func (u *UserHandle) AbsPath(rel string) string {
	if u == nil {
		return ""
	}
	return filepath.Join(u.DaemonDir, rel)
}

func (u *UserHandle) PushChange(change *remotefsv1.FileChange) {
	if u == nil || u.stream == nil {
		return
	}
	u.stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Change{Change: change},
	})
}

func (u *UserHandle) ReadRequestCount() int64 {
	if u == nil {
		return 0
	}
	return u.readCount.Load()
}

func (u *UserHandle) LastRequest() RequestSnapshot {
	if u == nil {
		return RequestSnapshot{}
	}

	u.requestMu.Lock()
	defer u.requestMu.Unlock()
	return u.lastRequest
}

func (u *UserHandle) SetFaults(faults FaultHooks) {
	if u == nil {
		return
	}

	u.faultMu.Lock()
	defer u.faultMu.Unlock()
	u.faults = faults
}

func (u *UserHandle) ResetFaults() {
	u.SetFaults(FaultHooks{})
}

func (u *UserHandle) WaitForPath(
	relPath string,
	timeout time.Duration,
	check func(*remotefsv1.FileInfo, bool) bool,
) bool {
	if u == nil || u.Session == nil {
		return false
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	if check == nil {
		check = func(_ *remotefsv1.FileInfo, ok bool) bool {
			return ok
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, ok := u.Session.Lookup(relPath)
		if check(info, ok) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}

	info, ok := u.Session.Lookup(relPath)
	return check(info, ok)
}

func (u *UserHandle) Disconnect() {
	if u == nil {
		return
	}
	if u.cancel != nil {
		u.cancel()
	}
}

func (u *UserHandle) Cleanup() {
	if u == nil {
		return
	}

	u.cleanupOnce.Do(func() {
		u.Disconnect()
		if u.loopDoneCh != nil {
			<-u.loopDoneCh
		}
		if u.serveErrCh != nil {
			<-u.serveErrCh
		}
		if u.Manager != nil && u.Session != nil {
			u.Manager.Remove(u.Session)
		}
		if u.harness != nil {
			u.harness.removeUser(u.UserID)
		}
	})
}

func ensureDaemonRoot(t testing.TB, daemonRoot string) string {
	t.Helper()

	if daemonRoot == "" {
		return t.TempDir()
	}
	require.NoError(t, os.MkdirAll(daemonRoot, 0o750))
	return daemonRoot
}

func startHandleLoop(
	ctx context.Context,
	user *UserHandle,
	handleOpts syncer.HandleOptions,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			req, ok := awaitRequest(ctx, user)
			if !ok {
				return
			}
			if req == nil {
				continue
			}
			if !handleRequest(ctx, user, req, handleOpts) {
				return
			}
		}
	}()
	return done
}

func awaitRequest(ctx context.Context, user *UserHandle) (*remotefsv1.FileRequest, bool) {
	msg, err := user.stream.AwaitSend(50 * time.Millisecond)
	if err != nil {
		select {
		case <-ctx.Done():
			return nil, false
		default:
			return nil, true
		}
	}
	return msg.GetRequest(), true
}

func handleRequest(
	ctx context.Context,
	user *UserHandle,
	req *remotefsv1.FileRequest,
	handleOpts syncer.HandleOptions,
) bool {
	user.recordRequest(req)

	resp, stop, handled := user.applyBeforeAction(ctx, req)
	if stop {
		return false
	}
	if handled {
		user.pushResponse(req, resp)
		return true
	}

	resp = syncer.Handle(req, handleOpts)
	resp, stop, send := user.applyAfterAction(ctx, req, resp)
	if stop {
		return false
	}
	if send {
		user.pushResponse(req, resp)
	}
	return true
}

func (u *UserHandle) recordRequest(req *remotefsv1.FileRequest) {
	if req == nil {
		return
	}
	if req.GetRead() != nil {
		u.readCount.Add(1)
	}

	u.requestMu.Lock()
	u.lastRequest = requestSnapshot(req)
	u.requestMu.Unlock()
}

func requestSnapshot(req *remotefsv1.FileRequest) RequestSnapshot {
	if req == nil {
		return RequestSnapshot{}
	}
	return RequestSnapshot{
		Kind: requestKind(req),
		Path: requestPath(req),
	}
}

func requestKind(req *remotefsv1.FileRequest) string {
	switch req.GetOperation().(type) {
	case *remotefsv1.FileRequest_Read:
		return "read"
	case *remotefsv1.FileRequest_Write:
		return "write"
	case *remotefsv1.FileRequest_Stat:
		return "stat"
	case *remotefsv1.FileRequest_ListDir:
		return "list_dir"
	case *remotefsv1.FileRequest_Delete:
		return "delete"
	case *remotefsv1.FileRequest_Mkdir:
		return "mkdir"
	case *remotefsv1.FileRequest_Rename:
		return "rename"
	case *remotefsv1.FileRequest_Truncate:
		return "truncate"
	default:
		return ""
	}
}

func requestPath(req *remotefsv1.FileRequest) string {
	switch op := req.GetOperation().(type) {
	case *remotefsv1.FileRequest_Read:
		return op.Read.GetPath()
	case *remotefsv1.FileRequest_Write:
		return op.Write.GetPath()
	case *remotefsv1.FileRequest_Stat:
		return op.Stat.GetPath()
	case *remotefsv1.FileRequest_ListDir:
		return op.ListDir.GetPath()
	case *remotefsv1.FileRequest_Delete:
		return op.Delete.GetPath()
	case *remotefsv1.FileRequest_Mkdir:
		return op.Mkdir.GetPath()
	case *remotefsv1.FileRequest_Rename:
		return op.Rename.GetOldPath() + "->" + op.Rename.GetNewPath()
	case *remotefsv1.FileRequest_Truncate:
		return op.Truncate.GetPath()
	default:
		return ""
	}
}

func (u *UserHandle) applyBeforeAction(
	ctx context.Context,
	req *remotefsv1.FileRequest,
) (*remotefsv1.FileResponse, bool, bool) {
	action := u.beforeAction(req)
	switch action.kind {
	case actionDelay:
		if !waitForDelay(ctx, action.delay) {
			return nil, true, false
		}
		return nil, false, false
	case actionDrop:
		u.Disconnect()
		return nil, true, false
	case actionRespond:
		return action.response, false, true
	default:
		return nil, false, false
	}
}

func (u *UserHandle) applyAfterAction(
	ctx context.Context,
	req *remotefsv1.FileRequest,
	resp *remotefsv1.FileResponse,
) (*remotefsv1.FileResponse, bool, bool) {
	action := u.afterAction(req, resp)
	switch action.kind {
	case actionDelay:
		if !waitForDelay(ctx, action.delay) {
			return nil, true, false
		}
		return resp, false, true
	case actionDrop:
		u.Disconnect()
		return nil, true, false
	case actionRespond:
		return action.response, false, true
	default:
		return resp, false, true
	}
}

func (u *UserHandle) beforeAction(req *remotefsv1.FileRequest) Action {
	u.faultMu.RLock()
	hook := u.faults.BeforeHandle
	u.faultMu.RUnlock()
	if hook == nil {
		return Pass()
	}
	return hook(req)
}

func (u *UserHandle) afterAction(req *remotefsv1.FileRequest, resp *remotefsv1.FileResponse) Action {
	u.faultMu.RLock()
	hook := u.faults.AfterHandle
	u.faultMu.RUnlock()
	if hook == nil {
		return Pass()
	}
	return hook(req, resp)
}

func waitForDelay(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (u *UserHandle) pushResponse(req *remotefsv1.FileRequest, resp *remotefsv1.FileResponse) {
	prepared := prepareResponse(req, resp)
	if prepared == nil {
		return
	}

	u.stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Response{Response: prepared},
	})
}

func prepareResponse(
	req *remotefsv1.FileRequest,
	resp *remotefsv1.FileResponse,
) *remotefsv1.FileResponse {
	if resp == nil {
		return nil
	}

	cloned, ok := proto.Clone(resp).(*remotefsv1.FileResponse)
	if !ok {
		return nil
	}
	if cloned.GetRequestId() == "" && req != nil {
		cloned.RequestId = req.GetRequestId()
	}
	return cloned
}
