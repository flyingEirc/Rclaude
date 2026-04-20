package ptyclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sync/errgroup"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

const defaultStdinChunk = 64 * 1024

// AttachParams populate the first attach frame sent on the stream.
type AttachParams struct {
	SessionID   string
	InitialSize WindowSize
	Term        string
}

// Config wires the client loop to its IO and transport collaborators.
type Config struct {
	Stream   Stream
	Stdin    io.ReadCloser
	Stdout   io.Writer
	Resizes  <-chan WindowSize
	Attach   AttachParams
	FrameMax int
}

// Client is a one-shot PTY bridge. Use New + Run; do not reuse.
type Client struct {
	cfg Config
}

type sendRequest struct {
	frame *remotefsv1.ClientFrame
	ack   chan error
}

// New returns a Client with nil-safe defaults for stdin/stdout/frame size.
func New(cfg Config) *Client {
	if cfg.FrameMax <= 0 {
		cfg.FrameMax = defaultStdinChunk
	}
	if cfg.Stdin == nil {
		cfg.Stdin = io.NopCloser(bytes.NewReader(nil))
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	return &Client{cfg: cfg}
}

// Run drives the bridge until the remote PTY exits, the server rejects the
// session, the stream fails, or ctx is canceled.
func (c *Client) Run(ctx context.Context) ExitResult {
	if c.cfg.Stream == nil {
		return ExitResult{Err: ErrNilStream}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	outbound := make(chan sendRequest, 16)
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer cancel()
		return c.sendLoop(gctx, outbound)
	})

	first, err := c.startSession(gctx, outbound)
	if err != nil {
		c.shutdownBackground(cancel, g)
		return ExitResult{Err: err}
	}

	if serverErr := first.GetError(); serverErr != nil {
		c.shutdownBackground(cancel, g)
		return ExitResult{ServerError: serverErr}
	}
	if first.GetAttached() == nil {
		c.shutdownBackground(cancel, g)
		return ExitResult{Err: ErrFirstFrameNotAttached}
	}

	g.Go(func() error {
		return c.stdinPump(gctx, outbound)
	})

	g.Go(func() error {
		return c.resizePump(gctx, outbound)
	})

	result := &ExitResult{}
	g.Go(func() error {
		return c.recvLoop(gctx, cancel, result)
	})

	waitErr := g.Wait()
	return buildExitResult(result, waitErr)
}

func (c *Client) sendLoop(ctx context.Context, outbound <-chan sendRequest) error {
	defer func() {
		c.closeSend()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case req, ok := <-outbound:
			if !ok {
				return nil
			}
			err := c.cfg.Stream.Send(req.frame)
			if req.ack != nil {
				req.ack <- err
				close(req.ack)
			}
			if err != nil {
				return err
			}
		}
	}
}

func (c *Client) stdinPump(ctx context.Context, outbound chan<- sendRequest) error {
	reader := bufio.NewReaderSize(c.cfg.Stdin, c.cfg.FrameMax)
	buf := make([]byte, c.cfg.FrameMax)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		n, readErr := reader.Read(buf)
		if err := c.forwardStdinChunk(ctx, outbound, buf[:n]); err != nil {
			return err
		}
		if readErr != nil {
			return normalizeReadErr(readErr)
		}
	}
}

func (c *Client) resizePump(ctx context.Context, outbound chan<- sendRequest) error {
	if c.cfg.Resizes == nil {
		<-ctx.Done()
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ws, ok := <-c.cfg.Resizes:
			if !ok {
				return nil
			}
			if err := enqueue(ctx, outbound, &remotefsv1.ClientFrame{
				Payload: &remotefsv1.ClientFrame_Resize{Resize: toProtoSize(ws)},
			}); err != nil {
				return err
			}
		}
	}
}

func enqueue(ctx context.Context, outbound chan<- sendRequest, frame *remotefsv1.ClientFrame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case outbound <- sendRequest{frame: frame}:
		return nil
	}
}

func enqueueSync(ctx context.Context, outbound chan<- sendRequest, frame *remotefsv1.ClientFrame) error {
	ack := make(chan error, 1)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case outbound <- sendRequest{frame: frame, ack: ack}:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-ack:
		return err
	}
}

func attachFrame(params AttachParams) *remotefsv1.ClientFrame {
	return &remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Attach{
			Attach: &remotefsv1.AttachReq{
				SessionId:   params.SessionID,
				InitialSize: toProtoSize(params.InitialSize),
				Term:        params.Term,
			},
		},
	}
}

func (c *Client) startSession(ctx context.Context, outbound chan<- sendRequest) (*remotefsv1.ServerFrame, error) {
	if err := enqueueSync(ctx, outbound, attachFrame(c.cfg.Attach)); err != nil {
		return nil, err
	}

	type recvResult struct {
		frame *remotefsv1.ServerFrame
		err   error
	}

	resultCh := make(chan recvResult, 1)
	go func() {
		frame, err := c.cfg.Stream.Recv()
		resultCh <- recvResult{frame: frame, err: err}
	}()

	select {
	case <-ctx.Done():
		c.closeSend()
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.frame, result.err
	}
}

func (c *Client) recvLoop(ctx context.Context, cancel context.CancelFunc, result *ExitResult) error {
	defer cancel()
	defer c.closeStdin()

	for {
		frame, recvErr := c.cfg.Stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return ErrStreamClosedUnexpectedly
			}
			return recvErr
		}

		done, err := c.handleServerFrame(frame, result)
		if err != nil || done {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

func (c *Client) handleServerFrame(frame *remotefsv1.ServerFrame, result *ExitResult) (bool, error) {
	switch payload := frame.GetPayload().(type) {
	case *remotefsv1.ServerFrame_Stdout:
		_, err := c.cfg.Stdout.Write(payload.Stdout)
		return false, err
	case *remotefsv1.ServerFrame_Exited:
		result.Code = payload.Exited.GetCode()
		result.Signal = payload.Exited.GetSignal()
		return true, nil
	case *remotefsv1.ServerFrame_Error:
		result.ServerError = payload.Error
		return true, nil
	default:
		return false, fmt.Errorf("ptyclient: unexpected server frame %T", payload)
	}
}

func buildExitResult(result *ExitResult, waitErr error) ExitResult {
	if result == nil {
		result = &ExitResult{}
	}
	if result.ServerError == nil && result.Code == 0 && result.Signal == 0 && waitErr != nil {
		result.Err = waitErr
	}
	return *result
}

func (c *Client) forwardStdinChunk(ctx context.Context, outbound chan<- sendRequest, chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	payload := append([]byte(nil), chunk...)
	return enqueue(ctx, outbound, &remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: payload},
	})
}

func normalizeReadErr(err error) error {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, context.Canceled):
		return nil
	default:
		return err
	}
}

func (c *Client) closeStdin() {
	if c.cfg.Stdin == nil {
		return
	}
	if err := c.cfg.Stdin.Close(); err != nil && !errors.Is(err, io.EOF) {
		return
	}
}

func (c *Client) shutdownBackground(cancel context.CancelFunc, g *errgroup.Group) {
	cancel()
	c.closeStdin()
	if g == nil {
		return
	}
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return
	}
}

func (c *Client) closeSend() {
	if err := c.cfg.Stream.CloseSend(); err != nil && !errors.Is(err, io.EOF) {
		return
	}
}

func toProtoSize(ws WindowSize) *remotefsv1.Resize {
	return &remotefsv1.Resize{
		Cols:   ws.Cols,
		Rows:   ws.Rows,
		XPixel: ws.XPixel,
		YPixel: ws.YPixel,
	}
}
