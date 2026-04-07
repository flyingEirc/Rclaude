//go:build !linux

package fusefs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/session"
)

func TestMountUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	mounted, err := Mount(context.Background(), Options{
		Mountpoint: "C:/tmp/rclaude",
		Manager:    session.NewManager(),
	})
	require.Error(t, err)
	assert.Nil(t, mounted)
	assert.ErrorIs(t, err, ErrUnsupportedPlatform)
}
