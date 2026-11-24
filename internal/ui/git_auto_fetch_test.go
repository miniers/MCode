package mcodeui

import (
	"testing"
	"time"

	"mcode/internal/config"
)

// gitAutoFetchSetup 返回带已缓存 Git 状态的窗口状态；root 每个用例唯一，避免全局节流表串扰。
func gitAutoFetchSetup(t *testing.T, remote string) (*chatState, *gitPaneFakeClient) {
	t.Helper()
	s, ps, client := gitPaneTestState(t)
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.git[ps.threadID] = GitState{WorkDir: t.TempDir(), RemoteURL: remote}
	chatHeaderMu.Unlock()
	return s, client
}

func gitAutoFetchCalls(client *gitPaneFakeClient) int {
	client.paneMu.Lock()
	defer client.paneMu.Unlock()
	n := 0
	for _, call := range client.calls {
		if call == "fetch" {
			n++
		}
	}
	return n
}

func TestGitAutoFetchTriggersOnce(t *testing.T) {
	s, client := gitAutoFetchSetup(t, "git@example.com:a/b.git")
	if !s.gitAutoFetch() {
		t.Fatal("开关开启且有远端时应发起抓取")
	}
	gitPaneWait(t, func() bool { return gitAutoFetchCalls(client) == 1 })
}

func TestGitAutoFetchDisabled(t *testing.T) {
	s, client := gitAutoFetchSetup(t, "git@example.com:a/b.git")
	off := false
	cfg := config.Defaults()
	cfg.SourceControl.AutoFetch = &off
	setChatSettings(s, &SettingsState{cfg: cfg})
	t.Cleanup(func() { setChatSettings(s, nil) })
	if s.gitAutoFetch() || gitAutoFetchCalls(client) != 0 {
		t.Fatal("开关关闭时不应抓取")
	}
}

func TestGitAutoFetchNoRemote(t *testing.T) {
	s, client := gitAutoFetchSetup(t, "")
	if s.gitAutoFetch() || gitAutoFetchCalls(client) != 0 {
		t.Fatal("无远端时不应抓取")
	}
}

func TestGitAutoFetchThrottled(t *testing.T) {
	s, client := gitAutoFetchSetup(t, "git@example.com:a/b.git")
	if !s.gitAutoFetch() {
		t.Fatal("首次应发起抓取")
	}
	gitPaneWait(t, func() bool { return gitAutoFetchCalls(client) == 1 })
	if s.gitAutoFetch() {
		t.Fatal("5 分钟内重复触发不应再次抓取")
	}
	if got := gitAutoFetchCalls(client); got != 1 {
		t.Fatalf("GitFetch 调用 %d 次，want 1", got)
	}
}

func TestGitAutoFetchClaimWindow(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1000, 0)
	if !gitAutoFetchClaim(root, now) || gitAutoFetchClaim(root, now.Add(4*time.Minute)) || !gitAutoFetchClaim(root, now.Add(5*time.Minute)) {
		t.Fatal("节流窗口应为 5 分钟")
	}
}
