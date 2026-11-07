package mcodeui

import (
	"os"
	"sync"
	"time"

	"mcode/internal/agentconf"
)

// skillCacheTTL 是单个项目目录技能扫描结果的缓存有效期（1 分钟，与参考实现一致）。
const skillCacheTTL = 60 * time.Second

// skillCacheEntry 是单个项目目录的技能扫描缓存。
type skillCacheEntry struct {
	skills   []agentconf.Skill
	loadedAt time.Time
	loading  bool
	err      error
}

// skillCache 按项目路径缓存技能列表。技能是磁盘扫描结果，与后端会话无关，
// 因此放包级缓存而不是 chatState 字段，避免为了缓存扩字段定义。
var skillCache = struct {
	mu      sync.Mutex
	entries map[string]*skillCacheEntry
}{entries: make(map[string]*skillCacheEntry)}

// skillsForProject 返回 projectPath 下缓存的技能列表，第二项表示缓存是否就绪。
// 未命中或已过期时在后台启动一次扫描，扫描结束后调用 s.invalidate() 触发重绘；
// 调用方需持有 s.mu（由 updateCompletion 在锁内调用），扫描协程自己加锁读写缓存。
func (s *chatState) skillsForProject(projectPath string) ([]agentconf.Skill, bool) {
	now := time.Now()

	skillCache.mu.Lock()
	if skillCache.entries == nil {
		skillCache.entries = make(map[string]*skillCacheEntry)
	}
	if entry := skillCache.entries[projectPath]; entry != nil {
		if entry.loading || now.Sub(entry.loadedAt) < skillCacheTTL {
			skills, ready := entry.skills, !entry.loading
			skillCache.mu.Unlock()
			return skills, ready
		}
	}
	entry := &skillCacheEntry{loading: true}
	skillCache.entries[projectPath] = entry
	skillCache.mu.Unlock()

	opts := agentconf.Options{
		Home:       skillScanHome(),
		ProjectDir: projectPath,
		DataDir:    resolveDataDirFallback(),
	}

	go func() {
		skills, err := scanSkills(opts)

		skillCache.mu.Lock()
		if cur := skillCache.entries[projectPath]; cur == entry {
			cur.loading = false
			cur.loadedAt = time.Now()
			cur.err = err
			if err == nil {
				cur.skills = skills
			}
		}
		skillCache.mu.Unlock()

		if s != nil {
			s.invalidate()
		}
	}()

	return nil, false
}

// skillScanHome 返回技能扫描用的用户主目录；解析失败时返回空字符串（扫描会跳过全局来源）。
func skillScanHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// resetSkillCache 清空技能缓存，仅供测试在替换 scanSkills 后隔离用例。
func resetSkillCache() {
	skillCache.mu.Lock()
	skillCache.entries = make(map[string]*skillCacheEntry)
	skillCache.mu.Unlock()
}

// projectPathForThreadLocked 返回会话所属项目的路径，待创建会话取 pendingThread 的项目。
// 调用方需持有 s.mu；会话或项目查不到时返回空字符串。
func (s *chatState) projectPathForThreadLocked(threadID string) string {
	projectID := s.projectIDForThreadLocked(threadID)
	if projectID == "" && s.pendingThread != nil {
		projectID = s.pendingThread.ProjectID
	}
	if projectID == "" {
		return ""
	}
	for i := range s.projects {
		if s.projects[i].ID == projectID {
			return s.projects[i].Path
		}
	}
	return ""
}
