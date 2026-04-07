package syncer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSensitiveFilter_Builtins(t *testing.T) {
	t.Parallel()

	filter, err := NewSensitiveFilter(nil)
	require.NoError(t, err)

	assert.True(t, filter.Match(".env"))
	assert.True(t, filter.Match(".env.local"))
	assert.True(t, filter.Match("certs/client.pem"))
	assert.True(t, filter.Match(".ssh/id_ed25519"))
	assert.True(t, filter.Match("dir/service_secret"))
	assert.True(t, filter.Match("dir/service_secret.txt"))

	assert.False(t, filter.Match("id_ed25519.pub"))
	assert.False(t, filter.Match("token.go"))
	assert.False(t, filter.Match("docs/secret_manager.md"))
}

func TestSensitiveFilter_CustomPatternsApplyToSubtrees(t *testing.T) {
	t.Parallel()

	filter, err := NewSensitiveFilter([]string{"secrets/**", "configs/*.local.env", "private"})
	require.NoError(t, err)

	assert.True(t, filter.Match("secrets"))
	assert.True(t, filter.Match("secrets/app/config.yaml"))
	assert.True(t, filter.Match("configs/dev.local.env"))
	assert.True(t, filter.Match("a/private/file.txt"))
	assert.False(t, filter.Match("configs/dev.env"))
	assert.False(t, filter.Match("public/file.txt"))
}

func TestSensitiveFilter_InvalidPattern(t *testing.T) {
	t.Parallel()

	_, err := NewSensitiveFilter([]string{"["})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidSensitivePattern)
}
