package mcodeui

import (
	"sync"
	"testing"
	"time"

	"mcode/internal/agentconf"
)

// waitSkillReady 轮询等待某项目目录的技能扫描完成，避免依赖真实磁盘扫描耗时。
func waitSkillReady(t *testing.T, s *chatState, projectPath string) []agentconf.Skill {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		skills, ready := s.skillsForProject(projectPath)
		s.mu.Unlock()
		if ready {
			return skills
		}
		if time.Now().After(deadline) {
			t.Fatalf("skills for %q did not load in time", projectPath)
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 技能缓存按项目路径隔离：不同项目各扫一次，同一项目在 TTL 内复用结果。
func TestSkillCacheScopedByProject(t *testing.T) {
	origScan := scanSkills
	resetSkillCache()
	t.Cleanup(func() {
		scanSkills = origScan
		resetSkillCache()
	})

	var mu sync.Mutex
	calls := map[string]int{}
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		mu.Lock()
		calls[opts.ProjectDir]++
		mu.Unlock()
		return []agentconf.Skill{{
			Agent: "claude",
			Scope: "project",
			Name:  "demo",
			Path:  opts.ProjectDir + "/demo/SKILL.md",
		}}, nil
	}

	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Name: "a", Path: "/repo/a"}, {ID: "p2", Name: "b", Path: "/repo/b"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}, {ID: "t2", ProjectID: "p2"}}

	s.mu.Lock()
	path1 := s.projectPathForThreadLocked("t1")
	path2 := s.projectPathForThreadLocked("t2")
	// 首次访问必须返回未就绪，让面板先显示 loading 行。
	_, ready := s.skillsForProject(path1)
	s.mu.Unlock()
	if ready {
		t.Fatalf("first lookup must be loading")
	}
	if path1 != "/repo/a" || path2 != "/repo/b" {
		t.Fatalf("project paths = %q / %q", path1, path2)
	}

	if skills := waitSkillReady(t, s, path1); len(skills) != 1 || skills[0].Name != "demo" {
		t.Fatalf("project a skills = %+v", skills)
	}
	waitSkillReady(t, s, path2)

	mu.Lock()
	gotA, gotB := calls["/repo/a"], calls["/repo/b"]
	mu.Unlock()
	if gotA != 1 || gotB != 1 {
		t.Fatalf("scans per project = %d / %d, want 1 / 1", gotA, gotB)
	}

	// TTL 内再次访问命中缓存，不再触发扫描。
	waitSkillReady(t, s, path1)
	mu.Lock()
	gotA = calls["/repo/a"]
	mu.Unlock()
	if gotA != 1 {
		t.Fatalf("cached project rescanned %d times", gotA)
	}
}

// resetSkillCache 必须清空全部条目，否则用例之间会互相污染。
func TestResetSkillCache(t *testing.T) {
	resetSkillCache()
	skillCache.mu.Lock()
	skillCache.entries["/repo/x"] = &skillCacheEntry{loadedAt: time.Now()}
	skillCache.mu.Unlock()

	resetSkillCache()
	skillCache.mu.Lock()
	n := len(skillCache.entries)
	skillCache.mu.Unlock()
	if n != 0 {
		t.Fatalf("cache entries after reset = %d", n)
	}
}
