package fusefs

import (
	"context"
	"sort"
	"time"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/session"
)

func startPrefetch(_ context.Context, manager *session.Manager, userID string, infos []*remotefsv1.FileInfo) {
	current, err := requireSession(manager, userID)
	if err != nil || current.IsOfflineReadonly(time.Time{}) {
		return
	}

	candidates := prefetchCandidates(manager, current, infos)
	for _, info := range candidates {
		if info == nil || !current.TryStartPrefetch(info.GetPath()) {
			continue
		}
		go prefetchFile(manager, current, info)
	}
}

func prefetchCandidates(
	manager *session.Manager,
	current *session.Session,
	infos []*remotefsv1.FileInfo,
) []*remotefsv1.FileInfo {
	if manager == nil || current == nil || !manager.PrefetchEnabled() {
		return nil
	}
	if manager.CacheMaxBytes() <= 0 || manager.PrefetchMaxFileBytes() <= 0 || manager.PrefetchMaxFilesPerDir() <= 0 {
		return nil
	}

	sorted := make([]*remotefsv1.FileInfo, 0, len(infos))
	for _, info := range infos {
		if shouldPrefetchFile(manager, current, info) {
			sorted = append(sorted, info)
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].GetPath() < sorted[j].GetPath()
	})
	if len(sorted) > manager.PrefetchMaxFilesPerDir() {
		sorted = sorted[:manager.PrefetchMaxFilesPerDir()]
	}
	return sorted
}

func shouldPrefetchFile(
	manager *session.Manager,
	current *session.Session,
	info *remotefsv1.FileInfo,
) bool {
	if manager == nil || current == nil || info == nil {
		return false
	}
	if info.GetIsDir() || info.GetPath() == "" || info.GetSize() <= 0 {
		return false
	}
	if info.GetSize() > manager.PrefetchMaxFileBytes() || !shouldUseContentCache(manager, info) {
		return false
	}
	_, cached := current.GetCachedContent(info.GetPath(), info)
	return !cached
}

func prefetchFile(manager *session.Manager, current *session.Session, info *remotefsv1.FileInfo) {
	if current == nil || info == nil {
		return
	}

	relPath := info.GetPath()
	defer current.FinishPrefetch(relPath)

	if _, cached := current.GetCachedContent(relPath, info); cached {
		return
	}

	data, err := requestRead(context.Background(), manager, current, relPath, 0, 0)
	if err != nil {
		return
	}
	current.PutCachedContent(relPath, info, data)
}
