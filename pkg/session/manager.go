package session

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrNilSession  = errors.New("session: nil session")
	ErrEmptyUserID = errors.New("session: empty user id")
	ErrPTYBusy     = errors.New("session: pty already active")
)

// DaemonSession exposes the minimal live-daemon state PTY wiring needs.
type DaemonSession interface {
	UserID() string
	LastHeartbeat() time.Time
}

type ManagerOptions struct {
	RequestTimeout         time.Duration
	CacheMaxBytes          int64
	OfflineReadOnlyTTL     time.Duration
	PrefetchEnabled        bool
	PrefetchMaxFileBytes   int64
	PrefetchMaxFilesPerDir int
}

type Manager struct {
	mu                     sync.RWMutex
	sessions               map[string]*Session
	activePTY              map[string]string
	nextPTYID              uint64
	requestTimeout         time.Duration
	cacheMaxBytes          int64
	offlineReadOnlyTTL     time.Duration
	prefetchEnabled        bool
	prefetchMaxFileBytes   int64
	prefetchMaxFilesPerDir int
}

func NewManager(opts ...ManagerOptions) *Manager {
	var cfg ManagerOptions
	if len(opts) > 0 {
		cfg = opts[0]
	}

	return &Manager{
		sessions:               make(map[string]*Session),
		activePTY:              make(map[string]string),
		requestTimeout:         cfg.RequestTimeout,
		cacheMaxBytes:          cfg.CacheMaxBytes,
		offlineReadOnlyTTL:     cfg.OfflineReadOnlyTTL,
		prefetchEnabled:        cfg.PrefetchEnabled,
		prefetchMaxFileBytes:   cfg.PrefetchMaxFileBytes,
		prefetchMaxFilesPerDir: cfg.PrefetchMaxFilesPerDir,
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
	m.pruneExpiredLocked(time.Now())

	prev := m.sessions[s.UserID()]
	m.sessions[s.UserID()] = s
	return prev, nil
}

func (m *Manager) Get(userID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneExpiredLocked(time.Now())
	s, ok := m.sessions[userID]
	return s, ok
}

func (m *Manager) LookupDaemon(userID string) (DaemonSession, bool) {
	if m == nil || userID == "" {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneExpiredLocked(time.Now())
	current, ok := m.sessions[userID]
	if !ok || current == nil || sessionClosed(current) {
		return nil, false
	}
	return current, true
}

func (m *Manager) RegisterPTY(userID string) (string, error) {
	if m == nil {
		return "", ErrNilManager
	}
	if userID == "" {
		return "", ErrEmptyUserID
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.activePTY[userID]; exists {
		return "", ErrPTYBusy
	}

	sessionID := m.nextPTYSessionIDLocked(userID)
	m.activePTY[userID] = sessionID
	return sessionID, nil
}

func (m *Manager) UnregisterPTY(userID string, sessionID string) bool {
	if m == nil || userID == "" || sessionID == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current, ok := m.activePTY[userID]
	if !ok || current != sessionID {
		return false
	}

	delete(m.activePTY, userID)
	return true
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
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneExpiredLocked(time.Now())

	out := make([]string, 0, len(m.sessions))
	for userID := range m.sessions {
		out = append(out, userID)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) HandleDisconnect(current *Session, serveErr error) {
	if m == nil || current == nil || current.UserID() == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	live, ok := m.sessions[current.UserID()]
	if !ok || live != current {
		return
	}
	if errors.Is(serveErr, ErrSessionReplaced) || m.offlineReadOnlyTTL <= 0 {
		delete(m.sessions, current.UserID())
		return
	}

	current.RetainOffline(time.Now().Add(m.offlineReadOnlyTTL))
	m.pruneExpiredLocked(time.Now())
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

func (m *Manager) PrefetchEnabled() bool {
	if m == nil {
		return false
	}
	return m.prefetchEnabled
}

func (m *Manager) PrefetchMaxFileBytes() int64 {
	if m == nil {
		return 0
	}
	return m.prefetchMaxFileBytes
}

func (m *Manager) PrefetchMaxFilesPerDir() int {
	if m == nil {
		return 0
	}
	return m.prefetchMaxFilesPerDir
}

func (m *Manager) pruneExpiredLocked(now time.Time) {
	for userID, current := range m.sessions {
		if current != nil && current.IsExpired(now) {
			delete(m.sessions, userID)
		}
	}
}

func (m *Manager) nextPTYSessionIDLocked(userID string) string {
	m.nextPTYID++
	return fmt.Sprintf("%s-pty-%d", userID, m.nextPTYID)
}

func sessionClosed(current *Session) bool {
	if current == nil {
		return true
	}

	select {
	case <-current.closed:
		return true
	default:
		return false
	}
}
