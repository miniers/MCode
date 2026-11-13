package remote

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"mcode/internal/agentconf"
	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

// commandsForSkills 用给定的技能列表与 provider 打桩 scanSkills，调用一次 threads.commands 并返回结果。
// 复用 thread_ops_test.go 里的 fakeThreadOps 与 RPC 执行辅助，命令名集合由调用方断言。
func commandsForSkills(t *testing.T, provider string, mockSkills []agentconf.Skill) wire.ThreadsCommandsResult {
	t.Helper()
	testenv.TempHome(t)

	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	origScanSkills := scanSkills
	defer func() { scanSkills = origScanSkills }()
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return mockSkills, nil
	}

	fake.resolvedProvider = provider
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":900}`, newUUID())
	resp := execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.commands failed: %v", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal commands result: %v", err)
	}
	var res wire.ThreadsCommandsResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal commands result: %v", err)
	}
	return res
}

// commandNames 收集结果里的命令名，便于断言包含与顺序相关的场景。
func commandNames(res wire.ThreadsCommandsResult) []string {
	names := make([]string, 0, len(res.Commands))
	for _, c := range res.Commands {
		names = append(names, c.Name)
	}
	return names
}

// commandByName 按命令名查找命令，未命中返回 nil。
func commandByName(res wire.ThreadsCommandsResult, name string) *wire.RemoteCommand {
	for i := range res.Commands {
		if res.Commands[i].Name == name {
			return &res.Commands[i]
		}
	}
	return nil
}

// TestThreadCommandsSkills_DisabledSkipped 覆盖：停用的技能不出现在斜杠命令里。
func TestThreadCommandsSkills_DisabledSkipped(t *testing.T) {
	res := commandsForSkills(t, "claude", []agentconf.Skill{
		{Agent: "claude", Scope: "global", Name: "enabled-skill", Description: "可用技能", Enabled: true},
		{Agent: "claude", Scope: "global", Name: "disabled-skill", Description: "已停用技能", Disabled: true},
	})

	if commandByName(res, "enabled-skill") == nil {
		t.Fatalf("启用的技能应出现在命令列表，实际 %v", commandNames(res))
	}
	if cmd := commandByName(res, "disabled-skill"); cmd != nil {
		t.Fatalf("停用的技能不应出现在命令列表，实际 %+v", cmd)
	}
}

// TestThreadCommandsSkills_PluginSkillPrefixed 覆盖：claude 侧的插件技能以「插件名:技能名」出现。
func TestThreadCommandsSkills_PluginSkillPrefixed(t *testing.T) {
	res := commandsForSkills(t, "claude", []agentconf.Skill{
		{
			Agent:       "claude",
			Scope:       "external",
			Plugin:      "superpowers",
			Name:        "brainstorming",
			Description: "插件里的技能",
			Enabled:     true,
		},
	})

	cmd := commandByName(res, "superpowers:brainstorming")
	if cmd == nil {
		t.Fatalf("插件技能应以「插件名:技能名」出现，实际 %v", commandNames(res))
	}
	if cmd.Kind != "skill" || cmd.IsBuiltIn {
		t.Fatalf("插件技能应按普通技能命令下发，实际 %+v", cmd)
	}
	if cmd.Detail != "插件里的技能" {
		t.Fatalf("插件技能说明应透传，实际 %q", cmd.Detail)
	}
	if commandByName(res, "brainstorming") != nil {
		t.Fatalf("插件技能不应再以裸技能名出现，实际 %v", commandNames(res))
	}
}

// TestThreadCommandsSkills_PluginSkillOnlyForClaude 覆盖：非 claude 的 provider 不出现插件技能。
func TestThreadCommandsSkills_PluginSkillOnlyForClaude(t *testing.T) {
	pluginSkill := agentconf.Skill{
		Agent:       "claude",
		Scope:       "external",
		Plugin:      "superpowers",
		Name:        "brainstorming",
		Description: "插件里的技能",
		Enabled:     true,
	}

	res := commandsForSkills(t, "codex", []agentconf.Skill{pluginSkill})
	if cmd := commandByName(res, "superpowers:brainstorming"); cmd != nil {
		t.Fatalf("codex 下不应出现 claude 插件技能，实际 %+v", cmd)
	}
	if cmd := commandByName(res, "brainstorming"); cmd != nil {
		t.Fatalf("codex 下不应出现插件技能的裸名，实际 %+v", cmd)
	}
	if strings.Contains(strings.Join(commandNames(res), ","), "superpowers") {
		t.Fatalf("codex 下不应出现任何插件命令，实际 %v", commandNames(res))
	}
}

// TestThreadCommandsSkills_PluginSkillConflictKeepsBuiltin 覆盖：插件技能与内置命令同名时内置优先。
// 插件命令带插件前缀，所以裸名同名的内置命令必须完整保留，且整体不出现重复命令名。
func TestThreadCommandsSkills_PluginSkillConflictKeepsBuiltin(t *testing.T) {
	res := commandsForSkills(t, "claude", []agentconf.Skill{
		{
			Agent:       "claude",
			Scope:       "external",
			Plugin:      "superpowers",
			Name:        "mode", // 与内置命令 mode 同名
			Description: "撞名的插件技能",
			Enabled:     true,
		},
	})

	cmd := commandByName(res, "mode")
	if cmd == nil {
		t.Fatalf("内置 mode 必须保留，实际 %v", commandNames(res))
	}
	if !cmd.IsBuiltIn {
		t.Fatalf("同名场景下 mode 必须仍是内置命令，实际 %+v", cmd)
	}
	if cmd.Detail == "撞名的插件技能" {
		t.Fatalf("插件技能不得覆盖内置命令的说明")
	}
	if commandByName(res, "superpowers:mode") == nil {
		t.Fatalf("插件技能应以带前缀的命令名共存，实际 %v", commandNames(res))
	}

	seen := make(map[string]bool, len(res.Commands))
	for _, c := range res.Commands {
		if seen[c.Name] {
			t.Fatalf("命令名重复: %s", c.Name)
		}
		seen[c.Name] = true
	}
}

// TestThreadCommandsSkills_DedupByFullCommandName 覆盖：插件技能去重用完整命令名（插件名:技能名），
// 与名称恰好等于该完整名的普通技能互相去重，而非按裸技能名去重。
func TestThreadCommandsSkills_DedupByFullCommandName(t *testing.T) {
	res := commandsForSkills(t, "claude", []agentconf.Skill{
		{
			Agent:       "claude",
			Scope:       "external",
			Plugin:      "superpowers",
			Name:        "brainstorming",
			Description: "插件版本",
			Enabled:     true,
		},
		{
			Agent:       "claude",
			Scope:       "global",
			Name:        "superpowers:brainstorming",
			Description: "同名普通技能",
			Enabled:     true,
		},
	})

	hits := 0
	for _, name := range commandNames(res) {
		if name == "superpowers:brainstorming" {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("完整命令名相同应只保留一条，实际 %d 条（%v）", hits, commandNames(res))
	}
}

// TestThreadCommandsSkills_ScanOptionsCarryDataDir 覆盖：扫描技能时带上后端的数据目录与用户 Home，
// 才能与设置页读到同一份停用配置。
func TestThreadCommandsSkills_ScanOptionsCarryDataDir(t *testing.T) {
	homeDir := t.TempDir()
	dataDir := t.TempDir()

	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()
	fake.SetDataDir(dataDir)

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	origScanSkills := scanSkills
	defer func() { scanSkills = origScanSkills }()
	var capturedOpts agentconf.Options
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		capturedOpts = opts
		return nil, nil
	}

	testenv.SetHome(t, homeDir)
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":901}`, newUUID())
	if resp := execThreadOpsRPC(t, d, conn, req); resp.Error != nil {
		t.Fatalf("threads.commands failed: %v", resp.Error.Message)
	}

	if capturedOpts.DataDir != dataDir {
		t.Fatalf("扫描技能应带上后端数据目录 %q，实际 %q", dataDir, capturedOpts.DataDir)
	}
	if capturedOpts.Home == "" {
		t.Fatalf("扫描技能应带上用户 Home")
	}
	if capturedOpts.ProjectDir == "" {
		t.Fatalf("扫描技能应带上项目目录")
	}
}
