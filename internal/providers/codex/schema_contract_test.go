package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// appServerMethodsFile 是本包锁定的 codex app-server 方法清单快照文件名。
// 该快照由 `codex app-server generate-json-schema --experimental` 的导出物提取而来，
// 只保留方法名，不含完整 schema，因此体积小、可随代码一起评审。
const appServerMethodsFile = "app_server_methods_0165.json"

// appServerMethods 是方法清单快照的结构：一个版本、三类方法名数组。
type appServerMethods struct {
	Version             string   `json:"version"`
	ClientRequests      []string `json:"clientRequests"`
	ServerRequests      []string `json:"serverRequests"`
	ServerNotifications []string `json:"serverNotifications"`
}

// usedClientRequests 是本包向 app-server 发送的请求方法。
var usedClientRequests = []string{
	"initialize",
	"thread/start",
	"thread/resume",
	"turn/start",
	"turn/steer",
	"turn/interrupt",
	"model/list",
	"config/read",
	"account/read",
	"account/rateLimits/read",
	"thread/compact/start",
	"review/start",
}

// usedServerNotifications 是本包处理的服务端通知方法。
var usedServerNotifications = []string{
	"item/agentMessage/delta",
	"item/reasoning/textDelta",
	"item/reasoning/summaryTextDelta",
	"item/started",
	"item/completed",
	"thread/tokenUsage/updated",
	"turn/completed",
	"error",
	"item/plan/delta",
}

// usedServerRequests 是本包应答的服务端请求（审批类）方法。
var usedServerRequests = []string{
	"item/commandExecution/requestApproval",
	"item/fileChange/requestApproval",
	"applyPatchApproval",
	"execCommandApproval",
	"item/permissions/requestApproval",
}

// legacyServerNotifications 是「历史遗留」方法：0.165 版 schema 已移除，但本包仍保留处理分支，
// 以便兼容旧版 codex-cli。它们只是记录性检查，不构成契约，缺失不视为失败。
var legacyServerNotifications = []string{
	"turn/failed",
}

// loadAppServerMethods 读取并解析 testdata 下的方法清单快照。
func loadAppServerMethods(t *testing.T, name string) *appServerMethods {
	t.Helper()

	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取协议清单 %s 失败: %v", path, err)
	}
	var m appServerMethods
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析协议清单 %s 失败: %v", path, err)
	}
	return &m
}

// assertMethodsPresent 断言 wanted 中的每个方法都存在于 listed 中。
// 缺失时一次性列出全部缺失方法，并写明出错的方法类别。
func assertMethodsPresent(t *testing.T, category string, version string, listed, wanted []string) {
	t.Helper()

	present := make(map[string]struct{}, len(listed))
	for _, m := range listed {
		present[m] = struct{}{}
	}

	var missing []string
	for _, m := range wanted {
		if _, ok := present[m]; !ok {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	t.Fatalf("协议清单（codex-cli %s）中缺少 %s 方法: %v；说明该版本 app-server 已改名或移除这些方法，需同步修改本包实现与清单快照",
		version, category, missing)
}

// TestSchemaContractAppServerMethods 校验本包使用的方法都能在当前锁定的 codex-cli 版本清单里找到。
func TestSchemaContractAppServerMethods(t *testing.T) {
	m := loadAppServerMethods(t, appServerMethodsFile)
	if m.Version == "" {
		t.Fatalf("协议清单 %s 缺少 version 字段", appServerMethodsFile)
	}

	assertMethodsPresent(t, "ClientRequest", m.Version, m.ClientRequests, usedClientRequests)
	assertMethodsPresent(t, "ServerNotification", m.Version, m.ServerNotifications, usedServerNotifications)
	assertMethodsPresent(t, "ServerRequest", m.Version, m.ServerRequests, usedServerRequests)
}

// TestSchemaContractLegacyMethodsAbsent 记录历史遗留方法在当前版本中的存在情况，不构成失败条件。
func TestSchemaContractLegacyMethodsAbsent(t *testing.T) {
	m := loadAppServerMethods(t, appServerMethodsFile)

	present := make(map[string]struct{}, len(m.ServerNotifications))
	for _, name := range m.ServerNotifications {
		present[name] = struct{}{}
	}

	for _, name := range legacyServerNotifications {
		if _, ok := present[name]; ok {
			t.Logf("历史遗留通知 %q 仍出现在 codex-cli %s 的 ServerNotification 清单中，本包的处理分支继续有效", name, m.Version)
			continue
		}
		t.Logf("历史遗留通知 %q 不在 codex-cli %s 的 ServerNotification 清单中（该版本已移除），本包保留该处理分支仅为兼容旧版本，不视为契约失败", name, m.Version)
	}
}

// TestSchemaContractSnapshotWellFormed 校验快照文件本身：三类清单都已排序且无重复。
// 这样人工更新快照时留下的顺序错乱或重复项会被及时发现。
func TestSchemaContractSnapshotWellFormed(t *testing.T) {
	m := loadAppServerMethods(t, appServerMethodsFile)

	cases := []struct {
		category string
		methods  []string
	}{
		{"ClientRequest", m.ClientRequests},
		{"ServerRequest", m.ServerRequests},
		{"ServerNotification", m.ServerNotifications},
	}
	for _, c := range cases {
		if len(c.methods) == 0 {
			t.Errorf("%s 清单为空，疑似快照提取失败", c.category)
			continue
		}
		if !sort.StringsAreSorted(c.methods) {
			t.Errorf("%s 清单未按字典序排序，请重新提取快照", c.category)
		}
		for i := 1; i < len(c.methods); i++ {
			if c.methods[i] == c.methods[i-1] {
				t.Errorf("%s 清单存在重复方法 %q", c.category, c.methods[i])
			}
		}
		for _, name := range c.methods {
			if name == "" {
				t.Errorf("%s 清单存在空方法名", c.category)
			}
		}
	}
}
