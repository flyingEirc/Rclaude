package session

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
)

var (
	// ErrNilManager indicates that a gRPC service was created without a session manager.
	ErrNilManager = errors.New("session: nil manager")
	// ErrMissingUserID indicates that auth middleware did not inject user id into the stream context.
	ErrMissingUserID = errors.New("session: missing user id in context")
)

// Service implements the RemoteFS gRPC service on the server side.
type Service struct {
	remotefsv1.UnimplementedRemoteFSServer

	manager *Manager
}

// NewService constructs the RemoteFS stream service.
func NewService(manager *Manager) (*Service, error) {
	if manager == nil {
		return nil, ErrNilManager
	}
	return &Service{manager: manager}, nil
}

// Connect handles one daemon stream for one authenticated user.
func (s *Service) Connect(stream remotefsv1.RemoteFS_ConnectServer) error {
	userID, ok := auth.UserIDFromContext(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, ErrMissingUserID.Error())
	}

	initial, err := stream.Recv()
	if err != nil {
		return err
	}

	current := s.manager.NewSession(userID)
	if bootstrapErr := current.Bootstrap(initial); bootstrapErr != nil {
		return status.Error(codes.InvalidArgument, bootstrapErr.Error())
	}

	prev, err := s.manager.Register(current)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if prev != nil {
		prev.closeWithError(ErrSessionReplaced)
	}
	defer s.manager.Remove(current)

	return current.Serve(stream.Context(), stream)
}
