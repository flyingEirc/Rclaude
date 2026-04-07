package inmemtest

import (
	"testing"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/session"
)

type Pair struct {
	Manager   *session.Manager
	Session   *session.Session
	UserID    string
	DaemonDir string
	Cleanup   func()

	user *UserHandle
}

func Start(t *testing.T, daemonRoot string) *Pair {
	t.Helper()

	harness := NewHarness(t)
	user := harness.AddUser(UserOptions{
		UserID:     "u-test",
		DaemonRoot: daemonRoot,
	})

	return &Pair{
		Manager:   user.Manager,
		Session:   user.Session,
		UserID:    user.UserID,
		DaemonDir: user.DaemonDir,
		Cleanup:   harness.Cleanup,
		user:      user,
	}
}

func (p *Pair) AbsPath(rel string) string {
	if p == nil || p.user == nil {
		return ""
	}
	return p.user.AbsPath(rel)
}

func (p *Pair) PushChange(change *remotefsv1.FileChange) {
	if p == nil || p.user == nil {
		return
	}
	p.user.PushChange(change)
}

func (p *Pair) ReadRequestCount() int64 {
	if p == nil || p.user == nil {
		return 0
	}
	return p.user.ReadRequestCount()
}
