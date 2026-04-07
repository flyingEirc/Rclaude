package testutil

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// ErrAwaitSendTimeout indicates that no outbound server message arrived in time.
var ErrAwaitSendTimeout = errors.New("testutil: timed out waiting for outbound server message")

// MockConnectStream is a controllable in-memory implementation of RemoteFS_ConnectServer.
type MockConnectStream struct {
	ctx context.Context

	recvCh chan *remotefsv1.DaemonMessage
	sendCh chan *remotefsv1.ServerMessage

	mu      sync.Mutex
	header  metadata.MD
	trailer metadata.MD
}

// NewMockConnectStream creates a stream backed by channels for tests.
func NewMockConnectStream(ctx context.Context) *MockConnectStream {
	if ctx == nil {
		ctx = context.Background()
	}
	return &MockConnectStream{
		ctx:    ctx,
		recvCh: make(chan *remotefsv1.DaemonMessage, 16),
		sendCh: make(chan *remotefsv1.ServerMessage, 16),
	}
}

// PushRecv queues one inbound daemon message for the server side to receive.
func (s *MockConnectStream) PushRecv(msg *remotefsv1.DaemonMessage) {
	s.recvCh <- msg
}

// CloseRecv closes the inbound side, making Recv return io.EOF.
func (s *MockConnectStream) CloseRecv() {
	close(s.recvCh)
}

// AwaitSend waits for one outbound server message.
func (s *MockConnectStream) AwaitSend(timeout time.Duration) (*remotefsv1.ServerMessage, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-timer.C:
		return nil, ErrAwaitSendTimeout
	case msg := <-s.sendCh:
		return msg, nil
	}
}

// Context returns the stream context.
func (s *MockConnectStream) Context() context.Context {
	return s.ctx
}

// Send stores one outbound server message.
func (s *MockConnectStream) Send(msg *remotefsv1.ServerMessage) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.sendCh <- msg:
		return nil
	}
}

// Recv returns the next inbound daemon message.
func (s *MockConnectStream) Recv() (*remotefsv1.DaemonMessage, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case msg, ok := <-s.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

// SendMsg implements grpc.ServerStream.
func (s *MockConnectStream) SendMsg(m any) error {
	msg, ok := m.(*remotefsv1.ServerMessage)
	if !ok {
		return errors.New("testutil: SendMsg expected *ServerMessage")
	}
	return s.Send(msg)
}

// RecvMsg implements grpc.ServerStream.
func (s *MockConnectStream) RecvMsg(m any) error {
	msg, ok := m.(*remotefsv1.DaemonMessage)
	if !ok {
		return errors.New("testutil: RecvMsg expected *DaemonMessage")
	}
	received, err := s.Recv()
	if err != nil {
		return err
	}
	proto.Reset(msg)
	proto.Merge(msg, received)
	return nil
}

// SetHeader records stream headers for completeness.
func (s *MockConnectStream) SetHeader(md metadata.MD) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.header = metadata.Join(s.header, md)
	return nil
}

// SendHeader records stream headers for completeness.
func (s *MockConnectStream) SendHeader(md metadata.MD) error {
	return s.SetHeader(md)
}

// SetTrailer records trailer metadata for completeness.
func (s *MockConnectStream) SetTrailer(md metadata.MD) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trailer = metadata.Join(s.trailer, md)
}
