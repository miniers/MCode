package mcodeui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// settingsCopyCase 是一条已改写的设置说明及其对应标题，用于校验 4.1 写作规则里的长度与去重约束。
type settingsCopyCase struct {
	title string
	desc  string
}

// settingsCopyCases 收录本轮改写的全部设置说明（编排、供应商、模型、技能、MCP 范围内）。
var settingsCopyCases = []settingsCopyCase{
	// 编排页（子代理）
	{title: "工作空间模式", desc: "每个子代理在独立工作树里改代码，互不干扰"},
	{title: "并发上限", desc: "默认 4；超出的子代理排队等待"},
	{title: "最多派发轮次", desc: "主代理最多派发几轮子代理，0 为不限"},
	{title: "子代理运行超时", desc: "单个子代理最多运行 60 分钟，等你回答的时间不计入"},
	{title: "请求总时长预算", desc: "一次请求里全部子代理合计最多跑 180 分钟"},
	{title: "自动清理已完成子代理", desc: "面板只留进行中的子代理"},
	{title: "侧边栏自动隐藏空闲子代理", desc: "空闲子代理收起，点开侧边栏可展开"},
	{title: "无人回答时转给主代理", desc: "超时后由主代理先答，拿不准再交给你"},
	{title: "Codex 子会话历史", desc: "只归档成功完成的 Codex 子会话，可后续恢复"},
	{title: "自动委托", desc: "需要拆分任务时由主代理自动派给子代理"},
	{title: "合并前审查", desc: "先审阅再合入改动，关闭则汇报后直接合入"},
	{title: "自动合并", desc: "默认开启；全部完成后合并到当前分支并清理副本"},
	{title: "自动推送", desc: "默认关闭；开启后自动推送分支并创建 PR"},
	// 编排页（档位）
	{title: "只读模式", desc: "只读档子代理在独立工作树里运行，改动一律丢弃"},
	{title: "写入白名单", desc: "每行一个路径通配，留空则不限制"},
	// 供应商页
	{title: "名称", desc: "这个网关在列表里显示的名字"},
	{title: "接口地址（BaseURL）", desc: "例如 https://host/v1"},
	{title: "默认模型", desc: "不填则按请求里的 model 透传"},
	{title: "密钥（API Key）", desc: "只保存在本机配置文件"},
	{title: "人设管理地址", desc: "从这个地址读取人设列表"},
	{title: "手动人设列表", desc: "JSON 数组，高级用法"},
	// 供应商页（预设表单）
	{title: "接口地址（BaseURL）", desc: "留空则用供应商默认值"},
	// 供应商与 Agent 页
	{title: "监控更新", desc: "默认开启；有新版本时在列表顶部提示"},
	// 模型页
	{title: "采样温度", desc: "越高越随机，越低越稳定（0–2，默认 1）"},
	// Agy 多账号页
	{title: "调度策略", desc: "多个账号之间怎么轮换"},
	{title: "会话保持模式", desc: "同一会话尽量用同一账号"},
	{title: "冷却账号", desc: "额度用尽的账号排到调度队列末尾"},
	{title: "到期自动预热", desc: "额度恢复时自动尝试一次，确认可用"},
	// 锁定态统一说明（无独立标题）
	{title: "", desc: "由启动参数指定，重启后生效"},
}

// TestSettingsCopy_Rule41 校验改写后的说明：不超过 60 字（按 rune 计），且不以对应标题开头。
func TestSettingsCopy_Rule41(t *testing.T) {
	const maxRunes = 60
	for _, tc := range settingsCopyCases {
		desc := tc.desc
		if n := utf8.RuneCountInString(desc); n > maxRunes {
			t.Errorf("说明超过 %d 字（实际 %d 字）：%q", maxRunes, n, desc)
		}
		if desc == "" {
			t.Errorf("说明为空：标题 %q", tc.title)
			continue
		}
		if tc.title == "" {
			continue
		}
		if strings.HasPrefix(desc, tc.title) {
			t.Errorf("说明复述了标题 %q：%q", tc.title, desc)
		}
	}
}
