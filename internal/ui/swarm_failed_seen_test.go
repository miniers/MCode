package mcodeui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/testenv"
)

func resetSwarmFailedSeenTestState(t *testing.T) {
	t.Helper()
	waitUIStateWrites()
	clearSwarmFailedSeenTestGlobals()
	t.Cleanup(func() {
		waitUIStateWrites()
		clearSwarmFailedSeenTestGlobals()
	})
}

func clearSwarmFailedSeenTestGlobals() {
	swarmFailedSeenMu.Lock()
	swarmFailedSeen = make(map[string]map[string]string)
	swarmFailedSeenMu.Unlock()

	lastModelSelectionMu.Lock()
	swarmFailedSeenCache = nil
	swarmFailedSeenDirty = false
	swarmFailedSeenGeneration = 0
	lastModelSelectionPath = ""
	lastModelSelectionLoaded = false
	lastModelSelectionModTime = time.Time{}
	lastModelSelectionCheckedAt = time.Time{}
	lastSelectionWritesPending = 0
	lastModelSelectionMu.Unlock()
}

func TestSwarmFailedBadgeSeenCountingAndRerun(t *testing.T) {
	resetSwarmFailedSeenTestState(t)

	t1 := time.Date(2026, 11, 18, 9, 0, 0, 123456789, time.UTC)
	t2 := t1.Add(2 * time.Minute)
	t3 := t1.Add(5 * time.Minute)
	leadID := "lead-failed-seen-1"
	bees := []SwarmBee{
		{ThreadID: "b1", Name: "Aspen", Status: "failed", FinishedAt: &t1},
		{ThreadID: "b2", Name: "Birch", Status: "failed", FinishedAt: nil},
		{ThreadID: "b3", Name: "Cedar", Status: "completed", FinishedAt: &t1},
		{ThreadID: "b4", Name: "Dale", Status: "running"},
	}

	if got := swarmUnseenFailedCount(leadID, bees); got != 2 {
		t.Fatalf("未查看时失败计数 = %d，期望 2", got)
	}

	markSwarmFailedBeesSeen(nil, leadID, bees)
	if got := swarmUnseenFailedCount(leadID, bees); got != 0 {
		t.Fatalf("标记查看后失败计数 = %d，期望 0", got)
	}

	// b1 重新运行并再次失败（FinishedAt 变化），应重新计入角标；b2 仍为已查看。
	bees[0].FinishedAt = &t2
	if got := swarmUnseenFailedCount(leadID, bees); got != 1 {
		t.Fatalf("b1 FinishedAt 变化后失败计数 = %d，期望 1", got)
	}

	// b2 从 nil FinishedAt 变为非空 FinishedAt，也应重新计数。
	bees[1].FinishedAt = &t3
	if got := swarmUnseenFailedCount(leadID, bees); got != 2 {
		t.Fatalf("b2 FinishedAt 从 nil 变为非空后失败计数 = %d，期望 2", got)
	}
}

func TestSwarmFailedBadgeSeenHeaderButtonClick(t *testing.T) {
	resetSwarmFailedSeenTestState(t)
	dataDir := t.TempDir()
	t.Setenv("MCODE_DATA", dataDir)
	testenv.TempHome(t)

	t1 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	leadID := "t-header-failed"
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		swarmFloatMu.Unlock()
	})

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.selectedThreadID = leadID
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = leadID
	hp.setStateLocked(SwarmState{
		LeadThreadID: leadID,
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "b1", Name: "Aspen", Status: "failed", FinishedAt: &t1},
			{ThreadID: "b2", Name: "Birch", Status: "failed", FinishedAt: nil},
		},
	})
	swarmPanelMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmHeaderButton(c, s)
	}, 900, 600)

	tt.Frame()
	if !tt.HasText("2") {
		t.Fatalf("点击前红色失败角标应存在，当前文本：%v", tt.Texts())
	}

	if err := tt.Click("编排"); err != nil {
		if keyErr := tt.Click("chat-header-swarm-btn"); keyErr != nil {
			t.Fatalf("点击编排按钮失败: %v (key fallback: %v)", err, keyErr)
		}
	}
	tt.Frame()

	if tt.HasText("2") {
		t.Fatalf("点击编排按钮后红色失败角标应消失，当前文本：%v", tt.Texts())
	}
	if _, err := os.Stat(filepath.Join(dataDir, uiStateFileName)); !os.IsNotExist(err) {
		t.Fatalf("未开启持久化的测试窗口不应写盘 ui-state.json，stat err=%v", err)
	}
}

func TestSwarmFailedBadgeSeenSingleBeeAndPersistence(t *testing.T) {
	resetSwarmFailedSeenTestState(t)
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	testenv.TempHome(t)

	t1 := time.Date(2026, 11, 18, 11, 30, 0, 987654321, time.UTC)
	leadID := "lead-persist-1"
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		delete(swarmSelectedBeeID, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmBeeKeptOpen, leadID)
		swarmFloatMu.Unlock()
	})

	b1 := SwarmBee{ThreadID: "b1", Name: "Aspen", Status: "failed", FinishedAt: &t1}
	b2 := SwarmBee{ThreadID: "b2", Name: "Birch", Status: "failed", FinishedAt: nil}
	bees := []SwarmBee{b1, b2}

	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)

	// 模拟在时间轴/胶囊浮窗里点开第一个失败 bee。
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmCapsuleOpenFloatButton(c, s, leadID, b1, "cap-b1", nil)
	}, 400, 200)
	if err := tt.Click("打开浮窗"); err != nil {
		t.Fatalf("点击打开浮窗失败: %v", err)
	}
	tt.Frame()

	if got := swarmUnseenFailedCount(leadID, bees); got != 1 {
		t.Fatalf("点开 b1 后失败计数 = %d，期望 1", got)
	}

	// 再点开第二个 FinishedAt 为 nil 的失败 bee。
	markSwarmFailedBeeSeen(s, leadID, b2)
	if got := swarmUnseenFailedCount(leadID, bees); got != 0 {
		t.Fatalf("点开 b2 后失败计数 = %d，期望 0", got)
	}

	waitUIStateWrites()

	// 验证落盘 JSON 字段 swarm_failed_seen。
	raw, err := os.ReadFile(filepath.Join(dataDir, uiStateFileName))
	if err != nil {
		t.Fatalf("读取 ui-state.json 失败: %v", err)
	}
	var disk struct {
		SwarmFailedSeen map[string]map[string]string `json:"swarm_failed_seen"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("解析 ui-state.json 失败: %v", err)
	}
	if got := disk.SwarmFailedSeen[leadID]["b1"]; got != t1.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("落盘 b1 FinishedAt = %q，期望 %q", got, t1.UTC().Format(time.RFC3339Nano))
	}
	if got, ok := disk.SwarmFailedSeen[leadID]["b2"]; !ok || got != "" {
		t.Fatalf("落盘 b2 记录 = (%q, %v)，期望 (\"\", true)", got, ok)
	}

	// 其它写盘路径（如 saveLastModelSelection）不得冲掉 swarm_failed_seen。
	saveLastModelSelection("p1", ModelSelection{Provider: "codex", Model: "gpt-5.5"})
	waitUIStateWrites()

	// 清空内存模拟重启，通过 applyUIState 从磁盘恢复。
	clearSwarmFailedSeenTestGlobals()
	if got := swarmUnseenFailedCount(leadID, bees); got != 2 {
		t.Fatalf("清空内存后重启前应未恢复，got=%d", got)
	}
	s2 := newChatState(NewFakeClient())
	WithUIStatePersistence()(s2)
	applyUIState(s2, loadUIState())
	if got := swarmUnseenFailedCount(leadID, bees); got != 0 {
		t.Fatalf("applyUIState 恢复后失败计数 = %d，期望 0", got)
	}
}
