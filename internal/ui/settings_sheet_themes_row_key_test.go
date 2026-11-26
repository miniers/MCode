package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// 主题页的每个颜色行都要带索引里登记的 row key（row-theme-color-<key>），
// 否则设置搜索能搜到条目、跳转却找不到目标行而静默无响应。
func TestThemesSheetColorRowKeys(t *testing.T) {
	setAppReduceMotion(t, true)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Appearance.Theme = config.ThemeLight
	restoreActiveTheme(t)
	t.Cleanup(st.Close)
	tt := ui.NewTester(func(c *ui.Context) { renderThemesSheet(c, st) }, 1000, 4000)
	total := 0
	for _, section := range colorSections {
		for _, key := range section.keys {
			total++
			rowKey := themeColorRowPrefix + key
			if _, ok := rowKeyInAccess(tt, rowKey); !ok {
				t.Fatalf("主题页找不到颜色行 %q", rowKey)
			}
		}
	}
	if total != len(config.ThemeColorKeys) {
		t.Fatalf("colorSections 展开出 %d 个颜色键，期望 %d 个", total, len(config.ThemeColorKeys))
	}
}

// rowKeyInAccess 报告最后帧里是否有节点以 rowKey 为无障碍名且是分组/行角色。
// 行内有按钮或图片时，行本身的文字不进入 labels，tt.Find 只按文字找，找不到；
// 行上的 Label(rowKey) 才让它作为可访问名出现，设置搜索按这个名字定位。
func rowKeyInAccess(tt *ui.Tester, rowKey string) (ui.AccessInfo, bool) {
	for _, info := range tt.AccessAll() {
		if info.Name == rowKey {
			return info, true
		}
	}
	return ui.AccessInfo{}, false
}
