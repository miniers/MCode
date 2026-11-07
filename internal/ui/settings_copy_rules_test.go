package mcodeui

import (
	"testing"
	"unicode/utf8"
)

// 索引标题允许的最大字数。多数标题不超过 20 字；33 字这个上限来自
// row-provider-pi-vision-info，这一行把整句说明当标题用，短时间改不掉。
const settingsIndexTitleMaxRunes = 33

// settingsIndexTitleDupOK 是允许在同一 section 里重名的条目：Copilot 的
// 「已登录」「登录」两行互斥，同一时刻只会渲染一行。
var settingsIndexTitleDupOK = map[string]bool{
	"row-provider-copilot-account": true,
	"row-provider-copilot-login":   true,
}

// TestSettingsIndexCopyRules 锁定索引标题的写法：不超过长度上限，且同一页、同一
// sheet、同一 section 内不出现两个同名条目，否则搜索结果里会出现两条一模一样的行。
// 标题为空的条目是页面树里的自定义行（列表、说明等），由页面自己渲染标题，跳过。
func TestSettingsIndexCopyRules(t *testing.T) {
	seen := map[string]string{}
	for _, item := range settingsItems() {
		if item.Title == "" {
			continue
		}
		if n := utf8.RuneCountInString(item.Title); n > settingsIndexTitleMaxRunes {
			t.Errorf("%s: 标题 %d 字，超过上限 %d 字: %q", item.ID, n, settingsIndexTitleMaxRunes, item.Title)
		}
		key := item.Page + "\x00" + item.Sheet + "\x00" + item.Section + "\x00" + item.Title
		if prev, ok := seen[key]; ok && !settingsIndexTitleDupOK[prev] && !settingsIndexTitleDupOK[item.ID] {
			t.Errorf("%s 与 %s 在 %s/%s/%s 里重名: %q", prev, item.ID, item.Page, item.Sheet, item.Section, item.Title)
		}
		seen[key] = item.ID
	}
}
