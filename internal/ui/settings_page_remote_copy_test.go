package mcodeui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// remoteCopyCase 是一条设置项文案：标题与说明必须遵守同一套书写规则。
type remoteCopyCase struct {
	// where 是文案所属的源文件:行号，失败时便于定位。
	where string
	title string
	desc  string
}

// remoteCopyCases 收录记忆 / 版本控制 / 外观 / 归档 / 关于几页改写过的说明文案。
// 说明可能含多行，逐行检查。
var remoteCopyCases = []remoteCopyCase{
	{"internal/ui/settings_page_nowledge.go:17", "新会话注入 Working Memory", "默认开启；首轮把当前项目的记忆摘要写进提示词"},
	{"internal/ui/settings_page_nowledge.go:31", "向新会话说明记忆工具用法", "默认开启；首轮把记忆工具用法写进提示词"},
	{"internal/ui/settings_page_nowledge.go:45", "工作记忆注入字符上限", "默认 8000 字符，超出会截断"},
	{"internal/ui/settings_page_nowledge.go:76", "同步会话到 Nowledge", "默认开启；空闲 20 分钟或归档时自动同步，记忆仍需确认"},
	{"internal/ui/settings_page_nowledge.go:90", "同步编排子代理会话", "默认关闭；子代理会话多是任务说明，没有保存价值"},
	{"internal/ui/settings_page_nowledge.go:104", "同步自动化运行会话", "默认关闭；开启后自动化运行也会同步"},
	{"internal/ui/settings_page_nowledge.go:118", "派发子代理时附带相关记忆", "按任务检索最多 5 条记忆附在提示词后"},
	{"internal/ui/settings_page_source_control.go:103", "生成提交信息与会话标题的模型", "未选择时沿用 utility 角色模型"},
	{"internal/ui/settings_page_source_control.go:197", "新建工作树时初始化子模块", "默认开启；新工作树自动初始化子模块"},
	{"internal/ui/settings_page_source_control.go:219", "新建工作树时克隆依赖目录", "默认开启；把被忽略的依赖复制进新工作树，仅 APFS 生效"},
	{"internal/ui/settings_page_source_control.go:237", "复制的目录名单", "逗号分隔，支持 * 通配"},
	{"internal/ui/settings_page_appearance.go:18", "主题", "默认跟随系统，切换后立即生效"},
	{"internal/ui/settings_page_appearance.go:144", "代码字号", "代码块字号，独立于界面文字大小"},
	{"internal/ui/settings_page_archive.go:393", "无消息多少天后自动休眠", "默认 7 天，0 为不自动休眠"},
	{"internal/ui/settings_page_about.go:179", "检查更新", "立即检查是否有新版本"},
	{"internal/ui/settings_page_about.go:237", "导出诊断日志", "打包日志便于反馈问题，不含密钥"},
}

// TestSettingsCopyLength 说明单行不超过 60 字符，避免设置面板出现长篇大论。
func TestSettingsCopyLength(t *testing.T) {
	for _, tc := range remoteCopyCases {
		for _, line := range strings.Split(tc.desc, "\n") {
			if n := utf8.RuneCountInString(line); n > 60 {
				t.Errorf("%s 的说明过长（%d 字符）：%s", tc.where, n, line)
			}
		}
	}
}

// TestSettingsCopyNotRepeatTitle 说明不重复标题，否则用户读了两遍同一句话。
func TestSettingsCopyNotRepeatTitle(t *testing.T) {
	for _, tc := range remoteCopyCases {
		for _, line := range strings.Split(tc.desc, "\n") {
			if strings.HasPrefix(line, tc.title) {
				t.Errorf("%s 的说明以标题开头：%s", tc.where, line)
			}
		}
	}
}

// TestSettingsCopyNoMiddleDot 短说明里不使用「·」连接两个事实，拆句或分行。
func TestSettingsCopyNoMiddleDot(t *testing.T) {
	for _, tc := range remoteCopyCases {
		if strings.Contains(tc.desc, "·") {
			t.Errorf("%s 的说明含「·」：%s", tc.where, tc.desc)
		}
	}
}
