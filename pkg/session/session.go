package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/contentcache"
	"flyingEirc/Rclaude/pkg/fstree"
)

const sessionSendBufferSize = 64

var (
	// ErrNilRequest indicates that Request received a nil file request.
	ErrNilRequest = errors.New("session: nil file request")
	// ErrNilDaemonMessage indicates that Bootstrap received a nil daemon message.
	ErrNilDaemonMessage = errors.New("session: nil daemon message")
	// ErrMissingInitialFileTree indicates that the first daemon message was not a file tree.
	ErrMissingInitialFileTree = errors.New("session: first daemon message must contain file_tree")
	// ErrDuplicateRequestID indicates that a request id is already in flight.
	ErrDuplicateRequestID = errors.New("session: duplicate request id")
	// ErrSessionClosed indicates that the session is no longer usable.
	ErrSessionClosed = errors.New("session: session closed")
	// ErrSessionReplaced indicates that a newer connection replaced this session.
	ErrSessionReplaced = errors.New("session: session replaced by newer connection")
	// ErrSessionOffline indicates that the session is being retained as an offline read-only snapshot.
	ErrSessionOffline = errors.New("session: offline readonly")
)

// Session holds server-side state for one connected daemon.
type Session struct {
	userID string

	sendCh chan *remotefsv1.ServerMessage

	treeMu sync.RWMutex
	tree   *fstree.Tree
	cache  *contentcache.Cache

	prefetchMu  sync.Mutex
	prefetching map[string]struct{}

	pendingMu sync.Mutex
	pending   map[string]chan *remotefsv1.FileResponse

	stateMu      sync.RWMutex
	closeErr     error
	closed       chan struct{}
	offlineUntil time.Time

	reqSeq        atomic.Uint64
	lastHeartbeat atomic.Int64
}

type SessionOptions struct {
	CacheMaxBytes int64
}

// NewSession constructs a new session for one user id.
func NewSession(userID string, opts ...SessionOptions) *Session {
	var cfg SessionOptions
	if len(opts) > 0 {
		cfg = opts[0]
	}

	return &Session{
		userID:      userID,
		sendCh:      make(chan *remotefsv1.ServerMessage, sessionSendBufferSize),
		tree:        fstree.New(),
		cache:       contentcache.New(cfg.CacheMaxBytes),
		prefetching: make(map[string]struct{}),
		pending:     make(map[string]chan *remotefsv1.FileResponse),
		closed:      make(chan struct{}),
	}
}

// UserID returns the owning user id.
func (s *Session) UserID() string {
	if s == nil {
		return ""
	}
	return s.userID
}

// LastHeartbeat returns the most recent liveness timestamp observed from the daemon.
func (s *Session) LastHeartbeat() time.Time {
	if s == nil {
		return time.Time{}
	}
	nanos := s.lastHeartbeat.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// Bootstrap consumes the mandatory initial file tree message and replaces the current tree snapshot.
func (s *Session) Bootstrap(msg *remotefsv1.DaemonMessage) error {
	if msg == nil {
		return ErrNilDaemonMessage
	}
	fileTree := msg.GetFileTree()
	if fileTree == nil {
		return ErrMissingInitialFileTree
	}
	if err := s.replaceTree(fileTree); err != nil {
		return err
	}
	s.clearContentCache()
	s.touchHeartbeat()
	return nil
}

// Serve handles the ongoing bidirectional stream after Bootstrap completed successfully.
func (s *Session) Serve(ctx context.Context, stream remotefsv1.RemoteFS_ConnectServer) error {
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- s.runSendLoop(runCtx, stream)
	}()

	recvErr := s.runRecvLoop(runCtx, stream)
	cancel()
	sendErr := <-sendDone

	finalErr := firstNonNil(recvErr, sendErr)
	s.closeWithError(finalErr)
	return finalErr
}

// Request sends one file request to the daemon and waits for the matching response.
func (s *Session) Request(ctx context.Context, req *remotefsv1.FileRequest) (*remotefsv1.FileResponse, error) {
	if s.IsOfflineReadonly(time.Time{}) {
		return nil, ErrSessionOffline
	}

	ctx, cloned, err := s.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	respCh, err := s.registerPending(cloned.GetRequestId())
	if err != nil {
		return nil, err
	}
	defer s.dropPending(cloned.GetRequestId())

	if err := s.enqueueRequest(ctx, cloned); err != nil {
		return nil, err
	}
	return s.awaitResponse(ctx, respCh)
}

func (s *Session) RetainOffline(until time.Time) bool {
	if s == nil || until.IsZero() {
		return false
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if errors.Is(s.closeErr, ErrSessionReplaced) {
		return false
	}

	s.offlineUntil = until
	s.closeErr = ErrSessionOffline
	return true
}

func (s *Session) IsOfflineReadonly(now time.Time) bool {
	if s == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()

	if s.offlineUntil.IsZero() {
		return false
	}
	return now.Before(s.offlineUntil)
}

func (s *Session) IsExpired(now time.Time) bool {
	if s == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.stateMu.RLock()
	defer s.stateMu.RUnlock()

	if s.offlineUntil.IsZero() {
		return false
	}
	return !now.Before(s.offlineUntil)
}

// Lookup returns the latest known metadata for the given relative path.
func (s *Session) Lookup(path string) (*remotefsv1.FileInfo, bool) {
	tree := s.currentTree()
	return tree.Lookup(path)
}

// List returns the latest known direct children for the given relative directory path.
func (s *Session) List(path string) ([]*remotefsv1.FileInfo, bool) {
	tree := s.currentTree()
	return tree.List(path)
}

func (s *Session) ApplyWriteResult(info *remotefsv1.FileInfo) {
	if s == nil || info == nil {
		return
	}
	if err := s.currentTree().Insert(info); err != nil {
		return
	}
	s.invalidateForInfo(info)
}

func (s *Session) ApplyDelete(relPath string) {
	if s == nil {
		return
	}
	tree := s.currentTree()
	info, _ := tree.Lookup(relPath)
	tree.Delete(relPath)
	s.invalidatePath(relPath, isDirectory(info))
}

func (s *Session) ApplyRename(oldRel string, newInfo *remotefsv1.FileInfo) {
	if s == nil {
		return
	}
	tree := s.currentTree()
	oldInfo, _ := tree.Lookup(oldRel)
	tree.Delete(oldRel)
	if newInfo != nil {
		if err := tree.Insert(newInfo); err != nil {
			return
		}
	}
	s.invalidatePath(oldRel, isDirectory(oldInfo))
	s.invalidateForInfo(newInfo)
}

func (s *Session) GetCachedContent(relPath string, info *remotefsv1.FileInfo) ([]byte, bool) {
	if s == nil || info == nil {
		return nil, false
	}
	return s.currentCache().Get(relPath, signatureFromInfo(info))
}

func (s *Session) PutCachedContent(relPath string, info *remotefsv1.FileInfo, content []byte) bool {
	if s == nil || info == nil {
		return false
	}
	return s.currentCache().Put(relPath, signatureFromInfo(info), content)
}

func (s *Session) InvalidateContent(relPath string) {
	if s == nil {
		return
	}
	s.currentCache().Invalidate(relPath)
}

func (s *Session) InvalidateContentPrefix(relPath string) {
	if s == nil {
		return
	}
	s.currentCache().InvalidatePrefix(relPath)
}

func (s *Session) TryStartPrefetch(relPath string) bool {
	if s == nil || relPath == "" {
		return false
	}

	s.prefetchMu.Lock()
	defer s.prefetchMu.Unlock()

	if _, exists := s.prefetching[relPath]; exists {
		return false
	}
	s.prefetching[relPath] = struct{}{}
	return true
}

func (s *Session) FinishPrefetch(relPath string) {
	if s == nil || relPath == "" {
		return
	}

	s.prefetchMu.Lock()
	defer s.prefetchMu.Unlock()
	delete(s.prefetching, relPath)
}

func (s *Session) runSendLoop(ctx context.Context, stream remotefsv1.RemoteFS_ConnectServer) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-s.sendCh:
			if msg == nil {
				continue
			}
			if err := stream.Send(msg); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("session: stream send: %w", err)
			}
		}
	}
}

func (s *Session) runRecvLoop(ctx context.Context, stream remotefsv1.RemoteFS_ConnectServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return nilOrWrapped(err, "session: stream recv")
		}
		if err := s.handleDaemonMessage(msg); err != nil {
			return err
		}
	}
}

func (s *Session) handleDaemonMessage(msg *remotefsv1.DaemonMessage) error {
	if msg == nil {
		return ErrNilDaemonMessage
	}
	s.touchHeartbeat()

	switch body := msg.GetMsg().(type) {
	case *remotefsv1.DaemonMessage_FileTree:
		return s.replaceTree(body.FileTree)
	case *remotefsv1.DaemonMessage_Change:
		return s.applyChange(body.Change)
	case *remotefsv1.DaemonMessage_Response:
		s.resolvePending(body.Response)
		return nil
	case *remotefsv1.DaemonMessage_Heartbeat:
		return nil
	default:
		return nil
	}
}

func (s *Session) replaceTree(fileTree *remotefsv1.FileTree) error {
	next := fstree.New()
	for _, info := range fileTree.GetFiles() {
		if err := next.Insert(info); err != nil {
			return fmt.Errorf("session: build file tree: %w", err)
		}
	}

	s.treeMu.Lock()
	defer s.treeMu.Unlock()
	s.tree = next
	return nil
}

func (s *Session) applyChange(change *remotefsv1.FileChange) error {
	tree := s.currentTree()
	if change == nil {
		return fmt.Errorf("session: apply change: %w", fstree.ErrNilChange)
	}

	switch change.GetType() {
	case remotefsv1.ChangeType_CHANGE_TYPE_CREATE,
		remotefsv1.ChangeType_CHANGE_TYPE_MODIFY:
		if err := tree.Insert(change.GetFile()); err != nil {
			return fmt.Errorf("session: apply change: %w", err)
		}
		s.invalidateForInfo(change.GetFile())
		return nil
	case remotefsv1.ChangeType_CHANGE_TYPE_DELETE:
		target := change.GetFile().GetPath()
		oldInfo, _ := tree.Lookup(target)
		tree.Delete(target)
		s.invalidatePath(target, isDirectory(oldInfo))
		return nil
	case remotefsv1.ChangeType_CHANGE_TYPE_RENAME:
		oldPath := change.GetOldPath()
		oldInfo, _ := tree.Lookup(oldPath)
		tree.Delete(oldPath)
		if err := tree.Insert(change.GetFile()); err != nil {
			return fmt.Errorf("session: apply change: %w", err)
		}
		s.invalidatePath(oldPath, isDirectory(oldInfo))
		s.invalidateForInfo(change.GetFile())
		return nil
	default:
		return fmt.Errorf("session: apply change: %w", fstree.ErrUnknownChangeType)
	}
}

func (s *Session) currentTree() *fstree.Tree {
	s.treeMu.RLock()
	defer s.treeMu.RUnlock()
	if s.tree == nil {
		return fstree.New()
	}
	return s.tree
}

func (s *Session) currentCache() *contentcache.Cache {
	s.treeMu.RLock()
	defer s.treeMu.RUnlock()
	if s.cache == nil {
		return contentcache.New(0)
	}
	return s.cache
}

func (s *Session) nextRequestID() string {
	seq := s.reqSeq.Add(1)
	return fmt.Sprintf("%s-%d", s.userID, seq)
}

func (s *Session) registerPending(requestID string) (chan *remotefsv1.FileResponse, error) {
	select {
	case <-s.closed:
		return nil, s.closeErrValue()
	default:
	}

	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	if _, exists := s.pending[requestID]; exists {
		return nil, ErrDuplicateRequestID
	}
	ch := make(chan *remotefsv1.FileResponse, 1)
	s.pending[requestID] = ch
	return ch, nil
}

func (s *Session) dropPending(requestID string) {
	s.pendingMu.Lock()
	ch, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.pendingMu.Unlock()

	if ok {
		close(ch)
	}
}

func (s *Session) resolvePending(resp *remotefsv1.FileResponse) {
	if resp == nil {
		return
	}

	s.pendingMu.Lock()
	ch, ok := s.pending[resp.GetRequestId()]
	if ok {
		delete(s.pending, resp.GetRequestId())
	}
	s.pendingMu.Unlock()

	if ok {
		ch <- resp
		close(ch)
	}
}

func (s *Session) prepareRequest(
	ctx context.Context,
	req *remotefsv1.FileRequest,
) (context.Context, *remotefsv1.FileRequest, error) {
	if req == nil {
		return nil, nil, ErrNilRequest
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cloned, ok := proto.Clone(req).(*remotefsv1.FileRequest)
	if !ok {
		return nil, nil, ErrNilRequest
	}
	if cloned.GetRequestId() == "" {
		cloned.RequestId = s.nextRequestID()
	}
	return ctx, cloned, nil
}

func (s *Session) enqueueRequest(ctx context.Context, req *remotefsv1.FileRequest) error {
	msg := &remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{Request: req},
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return s.closeErrValue()
	case s.sendCh <- msg:
		return nil
	}
}

func (s *Session) awaitResponse(
	ctx context.Context,
	respCh <-chan *remotefsv1.FileResponse,
) (*remotefsv1.FileResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, s.closeErrValue()
	case resp, ok := <-respCh:
		if !ok {
			return nil, s.closeErrValue()
		}
		return resp, nil
	}
}

func (s *Session) closeWithError(err error) {
	s.stateMu.Lock()
	select {
	case <-s.closed:
		s.stateMu.Unlock()
		return
	default:
	}
	if err == nil {
		err = ErrSessionClosed
	}
	s.closeErr = err
	close(s.closed)
	s.stateMu.Unlock()

	s.pendingMu.Lock()
	pending := s.pending
	s.pending = make(map[string]chan *remotefsv1.FileResponse)
	s.pendingMu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
}

func (s *Session) closeErrValue() error {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.closeErr == nil {
		return ErrSessionClosed
	}
	return s.closeErr
}

func (s *Session) touchHeartbeat() {
	s.lastHeartbeat.Store(time.Now().UnixNano())
}

func (s *Session) clearContentCache() {
	s.currentCache().Clear()
}

func (s *Session) invalidateForInfo(info *remotefsv1.FileInfo) {
	if info == nil {
		return
	}
	s.invalidatePath(info.GetPath(), info.GetIsDir())
}

func (s *Session) invalidatePath(relPath string, recursive bool) {
	if recursive {
		s.InvalidateContentPrefix(relPath)
		return
	}
	s.InvalidateContent(relPath)
}

func isDirectory(info *remotefsv1.FileInfo) bool {
	return info != nil && info.GetIsDir()
}

func signatureFromInfo(info *remotefsv1.FileInfo) contentcache.Signature {
	if info == nil {
		return contentcache.Signature{}
	}
	return contentcache.Signature{
		Size:    info.GetSize(),
		ModTime: info.GetModTime(),
	}
}

func firstNonNil(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func nilOrWrapped(err error, prefix string) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
