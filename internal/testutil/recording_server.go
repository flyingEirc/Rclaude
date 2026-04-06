package testutil

import (
	"errors"
	"sync"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// ErrNoActiveStream 表示 RecordingServer 还没被任何 Daemon 连接上。
var ErrNoActiveStream = errors.New("testutil: no active stream")

// RecordingServer 是一个线程安全的 RemoteFSServer mock。
// 它把 Daemon 发来的 DaemonMessage 全部存下来，
// 并允许测试代码主动从 Server 侧向 Daemon 下发 ServerMessage。
//
// 同一时刻只支持一个活动 stream（Phase 2 的单 Daemon 场景）。
type RecordingServer struct {
	remotefsv1.UnimplementedRemoteFSServer

	mu       sync.Mutex
	received []*remotefsv1.DaemonMessage
	stream   remotefsv1.RemoteFS_ConnectServer
	ready    chan struct{}
}

// NewRecordingServer 构造一个空的 RecordingServer。
func NewRecordingServer() *RecordingServer {
	return &RecordingServer{
		ready: make(chan struct{}),
	}
}

// Connect 实现 RemoteFSServer 接口。每条收到的消息会被 Received 暴露。
// Recv 出错时（如客户端 close）函数返回 nil，让 grpc 关闭连接。
func (s *RecordingServer) Connect(stream remotefsv1.RemoteFS_ConnectServer) error {
	s.mu.Lock()
	s.stream = stream
	// 第一次 Connect 时通知 WaitReady。
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
	s.mu.Unlock()

	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		s.mu.Lock()
		s.received = append(s.received, msg)
		s.mu.Unlock()
	}
}

// WaitReady 返回一个 channel，Connect 被调用后会被 close，
// 可用于测试等待 Daemon 建连完成。
func (s *RecordingServer) WaitReady() <-chan struct{} {
	return s.ready
}

// Received 返回当前为止收到的全部消息的切片浅拷贝。
func (s *RecordingServer) Received() []*remotefsv1.DaemonMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*remotefsv1.DaemonMessage, len(s.received))
	copy(out, s.received)
	return out
}

// SendRequest 在活动 stream 上下发一条 ServerMessage；没有活动 stream 时返回 ErrNoActiveStream。
func (s *RecordingServer) SendRequest(msg *remotefsv1.ServerMessage) error {
	s.mu.Lock()
	stream := s.stream
	s.mu.Unlock()
	if stream == nil {
		return ErrNoActiveStream
	}
	return stream.Send(msg)
}
