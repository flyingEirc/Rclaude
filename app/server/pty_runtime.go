package main

import (
	"context"
	"errors"
	"sync"

	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/ptyhost"
	"flyingEirc/Rclaude/pkg/ptyservice"
	"flyingEirc/Rclaude/pkg/ratelimit"
	"flyingEirc/Rclaude/pkg/session"
)

func newPTYService(cfg *config.ServerConfig, manager *session.Manager) (*ptyservice.Service, error) {
	if cfg == nil {
		return nil, nil
	}
	if manager == nil {
		return nil, session.ErrNilManager
	}

	return ptyservice.New(ptyservice.Config{
		Registry:     ptyRegistry{manager: manager},
		Spawner:      ptySpawner{},
		AttachLimit:  newAttachLimiterStore(cfg.PTY.RateLimit.AttachQPS, cfg.PTY.RateLimit.AttachBurst),
		InputLimit:   newInputLimiterStore(cfg.PTY.RateLimit.StdinBPS, cfg.PTY.RateLimit.StdinBurst),
		Binary:       cfg.PTY.Binary,
		Workspace:    cfg.PTY.WorkspaceRoot,
		EnvWhitelist: append([]string(nil), cfg.PTY.EnvPassthrough...),
		FrameMax:     cfg.PTY.FrameMaxBytes,
		GracefulStop: cfg.PTY.GracefulShutdownTimeout,
	})
}

type ptyRegistry struct {
	manager *session.Manager
}

func (r ptyRegistry) LookupDaemon(userID string) bool {
	if r.manager == nil {
		return false
	}
	_, ok := r.manager.LookupDaemon(userID)
	return ok
}

func (r ptyRegistry) RegisterPTY(userID string) (string, bool, error) {
	if r.manager == nil {
		return "", false, session.ErrNilManager
	}
	sessionID, err := r.manager.RegisterPTY(userID)
	if errors.Is(err, session.ErrPTYBusy) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sessionID, true, nil
}

func (r ptyRegistry) UnregisterPTY(userID string, sessionID string) {
	if r.manager == nil {
		return
	}
	_ = r.manager.UnregisterPTY(userID, sessionID)
}

type ptySpawner struct{}

func (ptySpawner) Spawn(req ptyhost.SpawnReq) (ptyservice.Host, error) {
	return ptyhost.Spawn(req)
}

type attachLimiterStore struct {
	mu       sync.Mutex
	rate     int
	burst    int
	limiters map[string]*ratelimit.Limiter
}

func newAttachLimiterStore(rate int, burst int) *attachLimiterStore {
	return &attachLimiterStore{
		rate:     rate,
		burst:    burst,
		limiters: make(map[string]*ratelimit.Limiter),
	}
}

func (s *attachLimiterStore) Wait(ctx context.Context, userID string) error {
	if s == nil {
		return nil
	}
	return s.limiterFor(userID).Wait(ctx, 1)
}

func (s *attachLimiterStore) limiterFor(userID string) *ratelimit.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limiter, ok := s.limiters[userID]; ok {
		return limiter
	}
	limiter := ratelimit.NewPTYAttachLimiter(s.rate, s.burst)
	s.limiters[userID] = limiter
	return limiter
}

type inputLimiterStore struct {
	mu       sync.Mutex
	rate     int64
	burst    int64
	limiters map[string]*ratelimit.ByteLimiter
}

func newInputLimiterStore(rate int64, burst int64) *inputLimiterStore {
	return &inputLimiterStore{
		rate:     rate,
		burst:    burst,
		limiters: make(map[string]*ratelimit.ByteLimiter),
	}
}

func (s *inputLimiterStore) Wait(ctx context.Context, userID string, n int) error {
	if s == nil {
		return nil
	}
	return s.limiterFor(userID).WaitBytes(ctx, n)
}

func (s *inputLimiterStore) limiterFor(userID string) *ratelimit.ByteLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limiter, ok := s.limiters[userID]; ok {
		return limiter
	}
	limiter := ratelimit.NewPTYStdinLimiter(s.rate, s.burst)
	s.limiters[userID] = limiter
	return limiter
}
