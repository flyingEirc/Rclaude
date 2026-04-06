package fstree

import (
	"errors"
	"path"
	"strings"
	"sync"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// 错误集合：调用方应使用 errors.Is 比较。
var (
	// ErrNilInfo 表示传入了 nil *FileInfo。
	ErrNilInfo = errors.New("fstree: nil FileInfo")
	// ErrNilChange 表示传入了 nil *FileChange。
	ErrNilChange = errors.New("fstree: nil FileChange")
	// ErrUnknownChangeType 表示 ChangeType 不被识别。
	ErrUnknownChangeType = errors.New("fstree: unknown ChangeType")
)

// node 是 Tree 内部的单个节点。
type node struct {
	info     *remotefsv1.FileInfo
	children map[string]struct{} // 仅目录节点用，存放直接子项 base name
}

// Tree 是线程安全的内存文件树，按 forward-slash 路径索引。
// 路径规则：相对路径，不含 leading "/"，"" 表示根。
type Tree struct {
	mu    sync.RWMutex
	nodes map[string]*node // path -> node；包含一个空 key "" 的根节点
}

// New 构造一棵空 Tree（含根节点）。
func New() *Tree {
	t := &Tree{nodes: make(map[string]*node)}
	t.nodes[""] = &node{
		info: &remotefsv1.FileInfo{
			Path:  "",
			IsDir: true,
		},
		children: make(map[string]struct{}),
	}
	return t
}

// Insert 插入或更新一个文件/目录条目。
// 父目录不存在时自动以占位 FileInfo 补齐（IsDir=true，Size=0）。
// info 为 nil 返回 ErrNilInfo。
func (t *Tree) Insert(info *remotefsv1.FileInfo) error {
	if info == nil {
		return ErrNilInfo
	}
	cleaned := normalize(info.GetPath())
	if cleaned == "" {
		// 根节点不可被业务 Insert 覆盖。
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.insertLocked(cleaned, info)
	return nil
}

// Delete 删除给定路径条目；若是目录则连带删除全部子项。
// path == "" 或不存在时为 no-op。
func (t *Tree) Delete(p string) {
	cleaned := normalize(p)
	if cleaned == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deleteLocked(cleaned)
}

// Lookup 按路径查找条目；不存在返回 nil/false。
// 返回的 *FileInfo 是内部存储指针的浅拷贝，调用方不应修改其字段。
func (t *Tree) Lookup(p string) (*remotefsv1.FileInfo, bool) {
	cleaned := normalize(p)
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.nodes[cleaned]
	if !ok {
		return nil, false
	}
	return cloneInfo(n.info), true
}

// List 列出某目录直接子项。"" 表示根。
// 若 path 不存在或不是目录返回 nil/false。
func (t *Tree) List(p string) ([]*remotefsv1.FileInfo, bool) {
	cleaned := normalize(p)
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.nodes[cleaned]
	if !ok || !n.info.GetIsDir() {
		return nil, false
	}
	out := make([]*remotefsv1.FileInfo, 0, len(n.children))
	for name := range n.children {
		childPath := joinPath(cleaned, name)
		if c, exists := t.nodes[childPath]; exists {
			out = append(out, cloneInfo(c.info))
		}
	}
	return out, true
}

// Apply 应用一条变更事件。
// CREATE / MODIFY → Insert；DELETE → Delete；RENAME → Delete(old) + Insert(file)。
func (t *Tree) Apply(change *remotefsv1.FileChange) error {
	if change == nil {
		return ErrNilChange
	}
	switch change.GetType() {
	case remotefsv1.ChangeType_CHANGE_TYPE_CREATE,
		remotefsv1.ChangeType_CHANGE_TYPE_MODIFY:
		return t.Insert(change.GetFile())
	case remotefsv1.ChangeType_CHANGE_TYPE_DELETE:
		if f := change.GetFile(); f != nil {
			t.Delete(f.GetPath())
		}
		return nil
	case remotefsv1.ChangeType_CHANGE_TYPE_RENAME:
		t.Delete(change.GetOldPath())
		return t.Insert(change.GetFile())
	case remotefsv1.ChangeType_CHANGE_TYPE_UNSPECIFIED:
		return ErrUnknownChangeType
	default:
		return ErrUnknownChangeType
	}
}

// Snapshot 返回所有非根节点的浅拷贝列表，顺序不保证。
func (t *Tree) Snapshot() []*remotefsv1.FileInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*remotefsv1.FileInfo, 0, len(t.nodes))
	for p, n := range t.nodes {
		if p == "" {
			continue
		}
		out = append(out, cloneInfo(n.info))
	}
	return out
}

// insertLocked 在持锁前提下插入节点；自动补齐祖先目录占位。
func (t *Tree) insertLocked(p string, info *remotefsv1.FileInfo) {
	t.ensureAncestors(p)
	stored := cloneInfo(info)
	stored.Path = p
	t.nodes[p] = &node{
		info:     stored,
		children: childrenMapForInfo(t.nodes[p], stored),
	}
	// 把自己注册到父目录的 children 中。
	parent, base := splitParent(p)
	if pn, ok := t.nodes[parent]; ok {
		pn.children[base] = struct{}{}
	}
}

// childrenMapForInfo 决定新节点是否需要 children map：
// 目录节点保留旧 children（若有），文件节点不需要。
func childrenMapForInfo(prev *node, info *remotefsv1.FileInfo) map[string]struct{} {
	if !info.GetIsDir() {
		return nil
	}
	if prev != nil && prev.children != nil {
		return prev.children
	}
	return make(map[string]struct{})
}

// ensureAncestors 把 p 所有祖先目录补齐为占位目录节点。
func (t *Tree) ensureAncestors(p string) {
	parent, _ := splitParent(p)
	if parent == "" {
		return
	}
	if _, ok := t.nodes[parent]; ok {
		return
	}
	t.ensureAncestors(parent)
	t.nodes[parent] = &node{
		info: &remotefsv1.FileInfo{
			Path:  parent,
			IsDir: true,
		},
		children: make(map[string]struct{}),
	}
	gp, base := splitParent(parent)
	if gn, ok := t.nodes[gp]; ok {
		gn.children[base] = struct{}{}
	}
}

// deleteLocked 在持锁前提下删除节点；目录递归扫 children。
func (t *Tree) deleteLocked(p string) {
	n, ok := t.nodes[p]
	if !ok {
		return
	}
	if n.info.GetIsDir() {
		for child := range n.children {
			t.deleteLocked(joinPath(p, child))
		}
	}
	delete(t.nodes, p)
	parent, base := splitParent(p)
	if pn, ok := t.nodes[parent]; ok {
		delete(pn.children, base)
	}
}

// normalize 把任意输入路径规范化为 "" 或不含 leading "/" 的相对路径。
// 不做越界检查（路径越界场景由 pkg/safepath 处理）。
func normalize(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "/" || p == "." {
		return ""
	}
	cleaned := path.Clean(p)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// splitParent 把 p 拆为 (parent, base)。
// 例如 "a/b/c" → ("a/b", "c")，"a" → ("", "a")。
func splitParent(p string) (string, string) {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return "", p
	}
	return p[:idx], p[idx+1:]
}

// joinPath 把父目录与 base 拼成新路径，根目录 ("") 时不加 leading slash。
func joinPath(parent, base string) string {
	if parent == "" {
		return base
	}
	return parent + "/" + base
}

// cloneInfo 返回 FileInfo 的浅拷贝（仅复制导出字段），
// 防止内部存储被外部代码修改。
func cloneInfo(in *remotefsv1.FileInfo) *remotefsv1.FileInfo {
	if in == nil {
		return nil
	}
	return &remotefsv1.FileInfo{
		Path:    in.GetPath(),
		Size:    in.GetSize(),
		ModTime: in.GetModTime(),
		IsDir:   in.GetIsDir(),
		Mode:    in.GetMode(),
	}
}
