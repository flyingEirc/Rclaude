package fusefs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
	"flyingEirc/Rclaude/internal/testutil"
)

// TestWorkspaceMapping_LocalProjectTreeAppearsUnderUserAndProject 是本阶段的
// 验收测试：本地项目根目录（此处目录名即项目名）下的嵌套结构，必须完整映射为
// 服务端 /workspace/{userid}/{project}/<相对路径> 视图，且项目名与本地目录名一致。
func TestWorkspaceMapping_LocalProjectTreeAppearsUnderUserAndProject(t *testing.T) {
	t.Parallel()

	root := testutil.NewTempWorkspace(t, map[string]string{
		"README.md":        "hello",
		"src/":             "",
		"src/app/":         "",
		"src/app/main.go":  "package main",
		"docs/guide.md":    "guide",
		"docs/img/":        "",
		"docs/img/a.png":   "png",
		".hidden/keep.txt": "kept",
	})
	project := filepath.Base(root)

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()
	user := harness.AddUser(inmemtest.UserOptions{
		UserID:        "alice",
		DaemonRoot:    root,
		WorkspaceName: project,
	})

	// 项目名层：/workspace/alice/ 下唯一的一层是本地项目目录名。
	got, err := workspaceNameFor(harness.Manager, user.UserID)
	require.NoError(t, err)
	assert.Equal(t, project, got)

	// 目录结构层：项目层以下的相对路径与本地一致。
	for _, rel := range []string{
		"README.md",
		"src", "src/app", "src/app/main.go",
		"docs/guide.md", "docs/img/a.png",
		".hidden/keep.txt",
	} {
		info, lookupErr := lookupInfo(harness.Manager, user.UserID, rel)
		require.NoError(t, lookupErr, "path %q must be mapped", rel)
		assert.Equal(t, rel, info.GetPath())
	}

	infos, err := listInfos(harness.Manager, user.UserID, "src")
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "src/app", infos[0].GetPath())
	assert.True(t, infos[0].GetIsDir())

	// 文件内容可经会话读回，证明映射不仅是元数据。
	data, err := readChunk(context.Background(), harness.Manager, user.UserID, "README.md", 0, 16)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))

	// 会话不在线的用户没有项目层。
	_, err = workspaceNameFor(harness.Manager, "nobody")
	assert.ErrorIs(t, err, ErrSessionOffline)
}

// TestWorkspaceMapping_ServerRejectsUnsafeProjectName 保证服务端目录命名约束：
// 项目名必须是单段安全路径，非法名字在 Bootstrap 即被拒绝。
func TestWorkspaceMapping_ServerRejectsUnsafeProjectName(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	for _, name := range []string{"", "..", "a/b", `a\b`, "a\x00b"} {
		current := harness.Manager.NewSession("mallory")
		err := current.Bootstrap(&remotefsv1.DaemonMessage{
			Msg: &remotefsv1.DaemonMessage_FileTree{
				FileTree: &remotefsv1.FileTree{WorkspaceName: name},
			},
		})
		require.Error(t, err, "workspace name %q must be rejected", name)
	}
}
