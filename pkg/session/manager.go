package session

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrNilSession  = errors.New("session: nil session")
	ErrEmptyUserID = errors.New("session: empty user id")
)

type ManagerOptions struct {
	RequestTimeout time.Duration
	CacheMaxBytes  int64
}

type Manager struct {
	mu             sync.RWMutex
	sessions       map[string]*Session
	requestTimeout time.Duration
	cacheMaxBytes  int64
}

func NewManager(opts ...ManagerOptions) *Manager {
	var cfg ManagerOptions
	if len(opts) > 0 {
		cfg = opts[0]
	}

	return &Manager{
		sessions:       make(map[string]*Session),
		requestTimeout: cfg.RequestTimeout,
		cacheMaxBytes:  cfg.CacheMaxBytes,
	}
}

func (m *Manager) NewSession(userID string) *Session {
	if m == nil {
		return NewSession(userID)
	}
	return NewSession(userID, SessionOptions{
		CacheMaxBytes: m.cacheMaxBytes,
	})
}

func (m *Manager) Register(s *Session) (*Session, error) {
	if s == nil {
		return nil, ErrNilSession
	}
	if s.UserID() == "" {
		return nil, ErrEmptyUserID
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev := m.sessions[s.UserID()]
	m.sessions[s.UserID()] = s
	return prev, nil
}

func (m *Manager) Get(userID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[userID]
	return s, ok
}

func (m *Manager) Remove(s *Session) {
	if s == nil || s.UserID() == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if current, ok := m.sessions[s.UserID()]; ok && current == s {
		delete(m.sessions, s.UserID())
	}
}

func (m *Manager) UserIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]string, 0, len(m.sessions))
	for userID := range m.sessions {
		out = append(out, userID)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) RequestTimeout() time.Duration {
	if m == nil {
		return 0
	}
	return m.requestTimeout
}

func (m *Manager) CacheMaxBytes() int64 {
	if m == nil {
		return 0
	}
	return m.cacheMaxBytes
}
