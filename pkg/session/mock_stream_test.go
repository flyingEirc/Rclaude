package session

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

var errAwaitSendTimeout = errors.New("session: timed out waiting for outbound server message")

type mockConnectStream struct {
	ctx context.Context

	recvCh chan *remotefsv1.DaemonMessage
	sendCh chan *remotefsv1.ServerMessage

	mu      sync.Mutex
	header  metadata.MD
	trailer metadata.MD
}

func newMockConnectStream(ctx context.Context) *mockConnectStream {
	if ctx == nil {
		ctx = context.Background()
	}
	return &mockConnectStream{
		ctx:    ctx,
		recvCh: make(chan *remotefsv1.DaemonMessage, 16),
		sendCh: make(chan *remotefsv1.ServerMessage, 16),
	}
}

func (s *mockConnectStream) PushRecv(msg *remotefsv1.DaemonMessage) {
	s.recvCh <- msg
}

func (s *mockConnectStream) CloseRecv() {
	close(s.recvCh)
}

func (s *mockConnectStream) AwaitSend(timeout time.Duration) (*remotefsv1.ServerMessage, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-timer.C:
		return nil, errAwaitSendTimeout
	case msg := <-s.sendCh:
		return msg, nil
	}
}

func (s *mockConnectStream) Context() context.Context {
	return s.ctx
}

func (s *mockConnectStream) Send(msg *remotefsv1.ServerMessage) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.sendCh <- msg:
		return nil
	}
}

func (s *mockConnectStream) Recv() (*remotefsv1.DaemonMessage, error) {
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

func (s *mockConnectStream) SendMsg(m any) error {
	msg, ok := m.(*remotefsv1.ServerMessage)
	if !ok {
		return errors.New("session: SendMsg expected *ServerMessage")
	}
	return s.Send(msg)
}

func (s *mockConnectStream) RecvMsg(m any) error {
	msg, ok := m.(*remotefsv1.DaemonMessage)
	if !ok {
		return errors.New("session: RecvMsg expected *DaemonMessage")
	}
	received, err := s.Recv()
	if err != nil {
		return err
	}
	proto.Reset(msg)
	proto.Merge(msg, received)
	return nil
}

func (s *mockConnectStream) SetHeader(md metadata.MD) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.header = metadata.Join(s.header, md)
	return nil
}

func (s *mockConnectStream) SendHeader(md metadata.MD) error {
	return s.SetHeader(md)
}

func (s *mockConnectStream) SetTrailer(md metadata.MD) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trailer = metadata.Join(s.trailer, md)
}
