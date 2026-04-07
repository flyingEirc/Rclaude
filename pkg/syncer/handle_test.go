package syncer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func TestHandle_NilRequest(t *testing.T) {
	resp := Handle(nil, HandleOptions{Root: t.TempDir()})
	require.NotNil(t, resp)
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "nil request")
}

func TestHandle_Read_Full(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello world"), 0o600))

	req := &remotefsv1.FileRequest{
		RequestId: "r1",
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{Path: "f.txt"},
		},
	}
	resp := Handle(req, HandleOptions{Root: root})
	require.True(t, resp.GetSuccess(), resp.GetError())
	assert.Equal(t, "r1", resp.GetRequestId())
	assert.Equal(t, []byte("hello world"), resp.GetContent())
}

func TestHandle_Read_OffsetAndLength(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello world"), 0o600))

	tests := []struct {
		name   string
		offset int64
		length int64
		want   string
	}{
		{"offset only", 6, 0, "world"},
		{"offset + length", 0, 5, "hello"},
		{"middle", 6, 3, "wor"},
		{"beyond end", 100, 0, ""},
		{"negative offset normalized", -5, 0, "hello world"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := Handle(&remotefsv1.FileRequest{
				RequestId: "x",
				Operation: &remotefsv1.FileRequest_Read{
					Read: &remotefsv1.ReadFileReq{
						Path:   "f.txt",
						Offset: tc.offset,
						Length: tc.length,
					},
				},
			}, HandleOptions{Root: root})
			require.True(t, resp.GetSuccess(), resp.GetError())
			assert.Equal(t, tc.want, string(resp.GetContent()))
		})
	}
}

func TestHandle_Read_MaxSizeCap(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.bin"), []byte("0123456789"), 0o600))

	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{Path: "big.bin"},
		},
	}, HandleOptions{Root: root, MaxReadSize: 4})
	require.True(t, resp.GetSuccess(), resp.GetError())
	assert.Equal(t, []byte("0123"), resp.GetContent())
}

func TestHandle_Read_PathEscape(t *testing.T) {
	root := t.TempDir()
	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{Path: "../etc/passwd"},
		},
	}, HandleOptions{Root: root})
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "unsafe path")
}

func TestHandle_Read_Missing(t *testing.T) {
	root := t.TempDir()
	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{Path: "missing.txt"},
		},
	}, HandleOptions{Root: root})
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "read")
}

func TestHandle_Stat_File(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "f.txt"), []byte("abc"), 0o600))

	resp := Handle(&remotefsv1.FileRequest{
		RequestId: "s1",
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: "f.txt"},
		},
	}, HandleOptions{Root: root})
	require.True(t, resp.GetSuccess(), resp.GetError())
	info := resp.GetInfo()
	require.NotNil(t, info)
	assert.Equal(t, "f.txt", info.GetPath())
	assert.Equal(t, int64(3), info.GetSize())
	assert.False(t, info.GetIsDir())
}

func TestHandle_Stat_Dir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "d"), 0o750))

	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: "d"},
		},
	}, HandleOptions{Root: root})
	require.True(t, resp.GetSuccess(), resp.GetError())
	assert.True(t, resp.GetInfo().GetIsDir())
}

func TestHandle_Stat_Missing(t *testing.T) {
	root := t.TempDir()
	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: "nope"},
		},
	}, HandleOptions{Root: root})
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "stat")
}

func TestHandle_ListDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte(""), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte(""), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "d"), 0o750))

	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_ListDir{
			ListDir: &remotefsv1.ListDirReq{Path: ""},
		},
	}, HandleOptions{Root: root})
	require.True(t, resp.GetSuccess(), resp.GetError())
	entries := resp.GetEntries().GetFiles()
	paths := make([]string, 0, len(entries))
	for _, f := range entries {
		paths = append(paths, f.GetPath())
	}
	assert.ElementsMatch(t, []string{"a.txt", "b.txt", "d"}, paths)
}

func TestHandle_ListDir_Subdir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(sub, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "inner.txt"), []byte(""), 0o600))

	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_ListDir{
			ListDir: &remotefsv1.ListDirReq{Path: "sub"},
		},
	}, HandleOptions{Root: root})
	require.True(t, resp.GetSuccess(), resp.GetError())
	entries := resp.GetEntries().GetFiles()
	require.Len(t, entries, 1)
	assert.Equal(t, "sub/inner.txt", entries[0].GetPath())
}

func TestHandle_ReadLikeSensitivePathsReturnNotExist(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ssh"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ssh", "id_ed25519"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "visible.txt"), []byte("ok"), 0o600))

	filter, err := NewSensitiveFilter(nil)
	require.NoError(t, err)
	opts := HandleOptions{Root: root, SensitiveFilter: filter}

	readResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{Path: ".env"},
		},
	}, opts)
	assert.False(t, readResp.GetSuccess())
	assert.Contains(t, readResp.GetError(), "no such file")

	statResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: ".ssh/id_ed25519"},
		},
	}, opts)
	assert.False(t, statResp.GetSuccess())
	assert.Contains(t, statResp.GetError(), "no such file")

	listResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_ListDir{
			ListDir: &remotefsv1.ListDirReq{Path: ""},
		},
	}, opts)
	require.True(t, listResp.GetSuccess(), listResp.GetError())
	paths := collectScanPaths(listResp.GetEntries().GetFiles())
	assert.ElementsMatch(t, []string{".ssh", "visible.txt"}, paths)
}

func TestHandle_Read_SensitiveSymlinkAliasReturnsNotExist(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(".env", filepath.Join(root, "visible.txt")))

	filter, err := NewSensitiveFilter(nil)
	require.NoError(t, err)

	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{Path: "visible.txt"},
		},
	}, HandleOptions{
		Root:            root,
		SensitiveFilter: filter,
	})

	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "no such file")
}

func TestHandle_ListDir_Missing(t *testing.T) {
	root := t.TempDir()
	resp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_ListDir{
			ListDir: &remotefsv1.ListDirReq{Path: "no-such"},
		},
	}, HandleOptions{Root: root})
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "list")
}

func TestHandle_WriteDeleteMkdirRenameTruncate(t *testing.T) {
	root := t.TempDir()

	writeResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{Path: "f.txt", Content: []byte("hello")},
		},
	}, HandleOptions{Root: root, Locker: newPathLocker(), SelfWrites: newSelfWriteFilter(time.Second)})
	require.True(t, writeResp.GetSuccess(), writeResp.GetError())

	mkdirResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Mkdir{
			Mkdir: &remotefsv1.MkdirReq{Path: "dir"},
		},
	}, HandleOptions{Root: root, Locker: newPathLocker(), SelfWrites: newSelfWriteFilter(time.Second)})
	require.True(t, mkdirResp.GetSuccess(), mkdirResp.GetError())

	renameResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Rename{
			Rename: &remotefsv1.RenameReq{OldPath: "f.txt", NewPath: "dir/f.txt"},
		},
	}, HandleOptions{Root: root, Locker: newPathLocker(), SelfWrites: newSelfWriteFilter(time.Second)})
	require.True(t, renameResp.GetSuccess(), renameResp.GetError())

	truncateResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Truncate{
			Truncate: &remotefsv1.TruncateReq{Path: "dir/f.txt", Size: 2},
		},
	}, HandleOptions{Root: root, Locker: newPathLocker(), SelfWrites: newSelfWriteFilter(time.Second)})
	require.True(t, truncateResp.GetSuccess(), truncateResp.GetError())

	deleteResp := Handle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Delete{
			Delete: &remotefsv1.DeleteReq{Path: "dir/f.txt"},
		},
	}, HandleOptions{Root: root, Locker: newPathLocker(), SelfWrites: newSelfWriteFilter(time.Second)})
	require.True(t, deleteResp.GetSuccess(), deleteResp.GetError())
}

func TestHandle_UnknownOperation(t *testing.T) {
	resp := Handle(&remotefsv1.FileRequest{RequestId: "u"}, HandleOptions{Root: t.TempDir()})
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "unknown")
}
