package mcodeui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// appShortcut 定义应用快捷键展示数据。
// bound 为 true 的条目带默认绑定，参与按键匹配与冲突检测；
// customizable 为 true 的条目可在设置页自定义键位；registered 为 true 的条目由
// dispatchActiveShortcut 每帧注册并派发（send-message 等由 composer 直接处理的条目不注册）。
type appShortcut struct {
	id           string
	title        string
	group        string
	keys         string
	mods         ui.Modifiers
	key          ui.Key
	bound        bool
	customizable bool
	registered   bool
}

// shortcutBinding 表示一条参与匹配与冲突检测的有效绑定。
type shortcutBinding struct {
	id         string
	mods       ui.Modifiers
	key        ui.Key
	registered bool
}

// shortcutDefaults 返回内置快捷键表（默认绑定，不含覆盖）。
func shortcutDefaults() []appShortcut {
	return []appShortcut{
		// 会话组
		{id: "new-thread", title: "新建会话", group: "会话", keys: "⌘N", mods: ui.Cmd, key: ui.KeyN, bound: true, customizable: true, registered: true},
		{id: "new-worktree-thread", title: "在 Worktree 新建会话", group: "会话", keys: "⌥⌘N", mods: ui.Cmd | ui.Alt, key: ui.KeyN, bound: true, customizable: true, registered: true},
		{id: "stop-reply", title: "停止当前回复", group: "会话", keys: "⌘.", mods: ui.Cmd, key: ui.KeyPeriod, bound: true}, // composer.go 写死 ⌘.，改键不会生效，故不开放自定义
		{id: "queue-message", title: "运行中加入队列", group: "会话", keys: "⌘↩"},
		{id: "send-message", title: "发送消息", group: "会话", keys: "↩"},
		{id: "new-line", title: "换行", group: "会话", keys: "⇧↩"},
		{id: "approve-request", title: "批准待处理审批", group: "会话", keys: "⌘↩"},
		{id: "toggle-plan-mode", title: "切换 Build/Plan 模式", group: "会话", keys: "⇧⌘P", mods: ui.Cmd | ui.Shift, key: ui.KeyP, bound: true, customizable: true, registered: true},
		{id: "previous-thread", title: "上一会话", group: "会话", keys: "⌥⌘↑", mods: ui.Cmd | ui.Alt, key: ui.KeyUp, bound: true, customizable: true, registered: true},
		{id: "next-thread", title: "下一会话", group: "会话", keys: "⌥⌘↓", mods: ui.Cmd | ui.Alt, key: ui.KeyDown, bound: true, customizable: true, registered: true},
		// ⌘↓ 与上一/下一会话同键不同修饰键，不与既有绑定冲突；待处理收件箱用未被占用的 ⇧⌘I。
		{id: "jump-to-latest", title: "跳到最新", group: "会话", keys: "⌘↓", mods: ui.Cmd, key: ui.KeyDown, bound: true, customizable: true, registered: true},
		{id: "open-attention-inbox", title: "打开待处理收件箱", group: "会话", keys: "⇧⌘I", mods: ui.Cmd | ui.Shift, key: ui.KeyI, bound: true, customizable: true, registered: true},
		// ⌘⇧S 已被休眠会话占用，交给总管改用 ⇧⌘T。
		{id: "steward-start-topic", title: "交给总管", group: "会话", keys: "⇧⌘T", mods: ui.Cmd | ui.Shift, key: ui.KeyT, bound: true, customizable: true, registered: true},
		{id: "forward-thread", title: "转发到新会话", group: "会话", keys: "⇧⌘D", mods: ui.Cmd | ui.Shift, key: ui.KeyD, bound: true, customizable: true, registered: true},
		{id: "run-script", title: "运行脚本", group: "会话", keys: "⌥⌘R", mods: ui.Cmd | ui.Alt, key: ui.KeyR, bound: true, customizable: true, registered: true},
		{id: "select-thread-1-9", title: "直达第 N 个会话", group: "会话", keys: "⌘1…9"},
		{id: "dormant-thread", title: "休眠/重新激活会话", group: "会话", keys: "⇧⌘S", mods: ui.Cmd | ui.Shift, key: ui.KeyS, bound: true, customizable: true, registered: true},
		// ⌘W 由「文件」菜单的自定义「关闭」项占用：文件面板有预览 tab 时关当前 tab，
		// 否则关窗口；归档仍用 ⌃⌘A。
		{id: "archive-thread", title: "归档会话", group: "会话", keys: "⌃⌘A", mods: ui.Cmd | ui.Ctrl, key: ui.KeyA, bound: true, customizable: true, registered: true},
		// ⌘F 默认未被占用（⇧⌘F 归文件面板）；与 ⌘K、⌘B 一样不进每帧注册集合，
		// 由 chat.go 按有效绑定直接检测，避免被 dispatchActiveShortcut 每帧抢占。
		{id: "find-in-thread", title: "会话内查找", group: "会话", keys: "⌘F", mods: ui.Cmd, key: ui.KeyF, bound: true, customizable: true},

		// 窗口组
		{id: "toggle-sidebar", title: "显示/隐藏侧边栏", group: "窗口", keys: "⌘B", mods: ui.Cmd, key: ui.KeyB, bound: true, customizable: true},
		{id: "command-palette", title: "命令面板", group: "窗口", keys: "⌘K", mods: ui.Cmd, key: ui.KeyK, bound: true, customizable: true},
		{id: "show-shortcuts", title: "显示快捷键", group: "窗口", keys: "⌘/", mods: ui.Cmd, key: ui.KeySlash, bound: true, customizable: true, registered: true},
		{id: "open-settings", title: "打开设置", group: "窗口", keys: "⌘,", mods: ui.Cmd, key: ui.KeyComma, bound: true, customizable: true, registered: true},
		{id: "close-settings", title: "关闭设置", group: "窗口", keys: "Esc"},
		{id: "toggle-diff", title: "显示/隐藏改动", group: "窗口", keys: "⌘D", mods: ui.Cmd, key: ui.KeyD, bound: true, customizable: true, registered: true},
		{id: "toggle-terminal", title: "显示/隐藏终端", group: "窗口", keys: "⌘J", mods: ui.Cmd, key: ui.KeyJ, bound: true, customizable: true, registered: true},
		{id: "toggle-preview", title: "显示/隐藏预览", group: "窗口", keys: "⇧⌘B", mods: ui.Cmd | ui.Shift, key: ui.KeyB, bound: true, customizable: true, registered: true},
		// ⇧⌘E 已被源码编辑器占用，文件面板改用 ⇧⌘F，菜单 Accelerator 同源。
		{id: "toggle-file-manager", title: "显示/隐藏文件面板", group: "窗口", keys: "⇧⌘F", mods: ui.Cmd | ui.Shift, key: ui.KeyF, bound: true, customizable: true, registered: true},
		{id: "open-editor", title: "源码编辑器", group: "窗口", keys: "⇧⌘E", mods: ui.Cmd | ui.Shift, key: ui.KeyE, bound: true, customizable: true, registered: true},
		{id: "toggle-activity", title: "动态视图", group: "窗口", keys: "⌥⌘U", mods: ui.Cmd | ui.Alt, key: ui.KeyU, bound: true, customizable: true, registered: true},
		{id: "add-project", title: "添加项目", group: "窗口", keys: "⇧⌘O", mods: ui.Cmd | ui.Shift, key: ui.KeyO, bound: true, customizable: true, registered: true},
	}
}

// fixedShortcutBindings 返回不在展示列表中的固定绑定（不受覆盖影响，保持既有行为）。
func fixedShortcutBindings() []shortcutBinding {
	return []shortcutBinding{
		{id: "toggle-context", mods: ui.Cmd | ui.Shift, key: ui.KeyK, registered: true},
		{id: "toggle-terminal", mods: ui.Ctrl, key: ui.KeyBackquote, registered: true},
		// Esc 参与匹配与冲突检测，但不注册：注册后会被 dispatchActiveShortcut 每帧抢占，
		// 吞掉输入框（如会话标题重命名）的 Esc 取消。
		{id: "close-or-escape", mods: 0, key: ui.KeyEscape},
	}
}

// selectThreadBindings 返回 ⌘1…9 直达会话的固定绑定。
func selectThreadBindings() []shortcutBinding {
	bindings := make([]shortcutBinding, 0, 9)
	for key := ui.Key1; key <= ui.Key9; key++ {
		bindings = append(bindings, shortcutBinding{id: "select-thread-n", mods: ui.Cmd, key: key, registered: true})
	}
	return bindings
}

// shortcutOverridesMu 保护快捷键覆盖表与注册表；覆盖在 UI 线程写入，测试亦可能直接调用。
var shortcutOverridesMu sync.RWMutex

// shortcutOverrides 保存当前生效的快捷键覆盖（快捷键 id -> 按键规格字符串）。
var shortcutOverrides map[string]string

// registeredShortcutsList 保存每帧注册的应用快捷键绑定，由有效绑定动态生成。
var registeredShortcutsList = registeredShortcutsFrom(effectiveShortcutBindingsFor(nil))

// setShortcutOverrides 应用配置中的快捷键覆盖并重建注册表（须在 UI 线程或测试中调用）。
// 传入空表表示恢复全部默认键位。
func setShortcutOverrides(m map[string]string) {
	overrides := map[string]string(nil)
	if len(m) > 0 {
		overrides = make(map[string]string, len(m))
		for id, spec := range m {
			overrides[id] = spec
		}
	}

	shortcutOverridesMu.Lock()
	shortcutOverrides = overrides
	registeredShortcutsList = registeredShortcutsFrom(effectiveShortcutBindingsFor(overrides))
	shortcutOverridesMu.Unlock()

	// 设置页输入框跟随已应用的覆盖，避免展示过期内容。
	syncShortcutDrafts(overrides)

	// 原生菜单的 Accelerator 同源于快捷键表，改键后须重建菜单（U-044）。
	refreshAppMenu()
}

// shortcutOverridesSnapshot 返回覆盖表的只读快照（表本身创建后不再修改）。
func shortcutOverridesSnapshot() map[string]string {
	shortcutOverridesMu.RLock()
	defer shortcutOverridesMu.RUnlock()
	return shortcutOverrides
}

// effectiveShortcutBindings 返回应用覆盖后的有效绑定表（解析失败的覆盖忽略并回退默认）。
func effectiveShortcutBindings() []shortcutBinding {
	return effectiveShortcutBindingsFor(shortcutOverridesSnapshot())
}

// effectiveShortcutBindingsFor 按给定覆盖表构建有效绑定表。
func effectiveShortcutBindingsFor(overrides map[string]string) []shortcutBinding {
	defaults := shortcutDefaults()
	bindings := make([]shortcutBinding, 0, len(defaults)+len(fixedShortcutBindings())+9)
	for _, sc := range defaults {
		if !sc.bound {
			continue
		}
		mods, key := resolveShortcutBinding(sc, overrides)
		bindings = append(bindings, shortcutBinding{
			id:         sc.id,
			mods:       mods,
			key:        key,
			registered: sc.registered,
		})
	}
	bindings = append(bindings, fixedShortcutBindings()...)
	bindings = append(bindings, selectThreadBindings()...)
	return bindings
}

// resolveShortcutBinding 返回条目的有效绑定：可自定义条目应用覆盖，缺失或解析失败时回退默认。
func resolveShortcutBinding(sc appShortcut, overrides map[string]string) (ui.Modifiers, ui.Key) {
	if !sc.customizable {
		return sc.mods, sc.key
	}
	spec, ok := overrides[sc.id]
	if !ok || strings.TrimSpace(spec) == "" {
		return sc.mods, sc.key
	}
	mods, key, err := parseShortcutSpec(spec)
	if err != nil {
		return sc.mods, sc.key
	}
	return mods, key
}

// registeredShortcutsFrom 从有效绑定生成每帧注册表。
func registeredShortcutsFrom(bindings []shortcutBinding) []registeredShortcut {
	list := make([]registeredShortcut, 0, len(bindings))
	for _, b := range bindings {
		if b.registered {
			list = append(list, registeredShortcut{mods: b.mods, key: b.key})
		}
	}
	return list
}

// appShortcuts 返回当前应用快捷键列表，keys 展示当前有效绑定。
func appShortcuts() []appShortcut {
	overrides := shortcutOverridesSnapshot()
	list := shortcutDefaults()
	for i := range list {
		if !list[i].bound {
			continue
		}
		mods, key := resolveShortcutBinding(list[i], overrides)
		list[i].keys = formatShortcutKeys(mods, key)
	}
	return list
}

// defaultShortcutKeys 返回条目默认绑定的展示键位，作为设置页输入框的占位文案。
func defaultShortcutKeys(sc appShortcut) string {
	if !sc.bound {
		return sc.keys
	}
	return formatShortcutKeys(sc.mods, sc.key)
}

// matchShortcut 将按键修饰符与按键码匹配到应用级快捷键 ID（基于有效绑定）。
// 针对 ⌘1~⌘9 直达第 N 个会话，返回 id="select-thread-n" 且 index 为 1~9。
// 未注册的组合返回 ok=false。
func matchShortcut(mods ui.Modifiers, key ui.Key) (id string, index int, ok bool) {
	return matchShortcutIn(effectiveShortcutBindings(), mods, key)
}

// shortcutBindingFor 返回指定快捷键 id 的当前有效绑定（基于有效绑定表）。
// 用于把硬编码的按键检测接入可自定义覆盖；id 不存在时返回 ok=false。
func shortcutBindingFor(id string) (ui.Modifiers, ui.Key, bool) {
	for _, b := range effectiveShortcutBindings() {
		if b.id == id {
			return b.mods, b.key, true
		}
	}
	return 0, ui.KeyUnknown, false
}

// matchShortcutIn 在给定绑定表中查找匹配项；出现重复绑定时取表中靠前的条目。
func matchShortcutIn(bindings []shortcutBinding, mods ui.Modifiers, key ui.Key) (id string, index int, ok bool) {
	for _, b := range bindings {
		if b.mods == mods && b.key == key {
			return b.id, shortcutBindingIndex(b), true
		}
	}
	return "", 0, false
}

// shortcutBindingIndex 返回绑定对应的会话序号：⌘1…9 为 1~9，其余为 0。
func shortcutBindingIndex(b shortcutBinding) int {
	if b.id == "select-thread-n" {
		return int(b.key-ui.Key1) + 1
	}
	return 0
}

// shortcutConflicts 返回每个 id 与之按键组合重复的其它 id（同一 id 的多重绑定不算冲突）。
func shortcutConflicts(bindings []shortcutBinding) map[string][]string {
	conflicts := make(map[string][]string)
	for i := 0; i < len(bindings); i++ {
		for j := i + 1; j < len(bindings); j++ {
			a, b := bindings[i], bindings[j]
			if a.id == b.id || a.mods != b.mods || a.key != b.key {
				continue
			}
			if !containsShortcutID(conflicts[a.id], b.id) {
				conflicts[a.id] = append(conflicts[a.id], b.id)
			}
			if !containsShortcutID(conflicts[b.id], a.id) {
				conflicts[b.id] = append(conflicts[b.id], a.id)
			}
		}
	}
	return conflicts
}

// containsShortcutID 判断 id 是否已在列表中。
func containsShortcutID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// shortcutTitle 返回快捷键 id 的展示标题（含未展示在设置页的固定绑定）。
func shortcutTitle(id string) string {
	for _, sc := range shortcutDefaults() {
		if sc.id == id {
			return sc.title
		}
	}
	switch id {
	case "select-thread-n":
		return "直达第 N 个会话"
	case "toggle-context":
		return "显示/隐藏项目文档"
	case "close-or-escape":
		return "关闭/退出"
	}
	return id
}

// shortcutTitleWithKey 返回带当前有效键位的提示文案，如「跳到最新（⌘↓）」；未绑定时只返回 title。
func shortcutTitleWithKey(id, title string) string {
	if mods, key, ok := shortcutBindingFor(id); ok {
		return title + "（" + formatShortcutKeys(mods, key) + "）"
	}
	return title
}

// shortcutReserved 表示一个系统或原生菜单保留的按键组合。
type shortcutReserved struct {
	mods ui.Modifiers
	key  ui.Key
}

// shortcutReservedMessage 是自定义快捷键命中系统保留组合时的提示文案。
const shortcutReservedMessage = "该组合为系统保留"

// shortcutReservedCombos 返回系统与原生菜单占用的组合：这些按键由 macOS 或菜单角色处理，
// 落到应用绑定上会静默失效或与系统动作同时触发，所以设置页拒绝把快捷键改成它们（J-06）。
func shortcutReservedCombos() []shortcutReserved {
	return []shortcutReserved{
		{mods: ui.Cmd, key: ui.KeyQ},               // 退出
		{mods: ui.Cmd, key: ui.KeyW},               // 关闭窗口
		{mods: ui.Cmd, key: ui.KeyH},               // 隐藏
		{mods: ui.Cmd, key: ui.KeyM},               // 最小化
		{mods: ui.Cmd, key: ui.KeyTab},             // 切换应用
		{mods: ui.Cmd, key: ui.KeySpace},           // 聚焦搜索
		{mods: ui.Cmd, key: ui.KeyComma},           // 系统偏好/打开设置
		{mods: ui.Cmd | ui.Alt, key: ui.KeyEscape}, // 强制退出
	}
}

// isReservedShortcut 判断组合是否属于系统或原生菜单保留的键位。
func isReservedShortcut(mods ui.Modifiers, key ui.Key) bool {
	for _, r := range shortcutReservedCombos() {
		if r.mods == mods && r.key == key {
			return true
		}
	}
	return false
}

// shortcutModifierSymbols 修饰键符号（⌘⌥⇧⌃）与对应修饰位，用于解析直接粘贴的符号形式。
var shortcutModifierSymbols = []struct {
	symbol string
	mod    ui.Modifiers
}{
	{"⌘", ui.Cmd},
	{"⌥", ui.Alt},
	{"⇧", ui.Shift},
	{"⌃", ui.Ctrl},
}

// parseShortcutSpec 解析按键规格字符串，如 "Cmd+Shift+P"，也接受符号形式（如 "⇧⌘P"）。
// 修饰键支持 Cmd/Command/Super/CmdOrCtrl、Ctrl/Control、Alt/Option、Shift，与按键用 + 连接；
// 按键支持 A-Z、0-9、逗号、句点、斜杠、反引号、方向键、Enter、Escape、Space，大小写不敏感。
func parseShortcutSpec(spec string) (ui.Modifiers, ui.Key, error) {
	return parseShortcutSpecWith(spec, parseShortcutKey)
}

// parseShortcutSpecReserved 与 parseShortcutSpec 相同，但额外接受 Tab 这类只能出现在
// 系统保留组合里的按键，用于在严格解析报「不支持的按键」之前识别 ⌘Tab 等保留键位。
func parseShortcutSpecReserved(spec string) (ui.Modifiers, ui.Key, error) {
	return parseShortcutSpecWith(spec, func(token string) (ui.Key, error) {
		if key, err := parseShortcutKey(token); err == nil {
			return key, nil
		}
		if strings.EqualFold(strings.TrimSpace(token), "tab") {
			return ui.KeyTab, nil
		}
		return ui.KeyUnknown, fmt.Errorf("不支持的按键：%s", token)
	})
}

// parseShortcutSpecWith 按给定的按键解析函数解析按键规格。
func parseShortcutSpecWith(spec string, parseKey func(string) (ui.Key, error)) (ui.Modifiers, ui.Key, error) {
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return 0, ui.KeyUnknown, fmt.Errorf("快捷键不能为空")
	}

	var mods ui.Modifiers
	for {
		matched := false
		for _, s := range shortcutModifierSymbols {
			if strings.HasPrefix(raw, s.symbol) {
				mods |= s.mod
				raw = strings.TrimPrefix(raw, s.symbol)
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ui.KeyUnknown, fmt.Errorf("快捷键缺少按键")
	}

	parts := strings.Split(raw, "+")
	keyPart := strings.TrimSpace(parts[len(parts)-1])
	for _, part := range parts[:len(parts)-1] {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		switch token {
		// CmdOrCtrl 与 Cmd 同义：应用只按平台主修饰键绑定，macOS 上是 ⌘。
		case "cmd", "command", "super", "meta", "cmdorctrl", "commandorcontrol":
			mods |= ui.Cmd
		case "ctrl", "control":
			mods |= ui.Ctrl
		case "alt", "option", "opt":
			mods |= ui.Alt
		case "shift":
			mods |= ui.Shift
		default:
			return 0, ui.KeyUnknown, fmt.Errorf("未知修饰键：%s", part)
		}
	}

	key, err := parseKey(keyPart)
	if err != nil {
		return 0, ui.KeyUnknown, err
	}
	return mods, key, nil
}

// shortcutModifierAliases 把修饰键别名折叠到规范写法（Cmd/Ctrl/Alt/Shift）。
var shortcutModifierAliases = map[string]string{
	"cmd": "Cmd", "command": "Cmd", "super": "Cmd", "meta": "Cmd",
	"cmdorctrl": "Cmd", "commandorcontrol": "Cmd",
	"ctrl": "Ctrl", "control": "Ctrl",
	"alt": "Alt", "option": "Alt", "opt": "Alt",
	"shift": "Shift",
}

// normalizeShortcutSpec 规范化用户输入的按键规格：去首尾空白，修饰键统一成规范别名
// 并忽略大小写，最后一段若是单个字母则转成大写（其余命名键保留原写法，
// 如 Tab/Space/Esc/F1；parseShortcutSpec 对按键大小写不敏感）。
// 符号形式（如 ⇧⌘P）没有 + 分隔符，原样返回。
func normalizeShortcutSpec(spec string) string {
	raw := strings.TrimSpace(spec)
	if raw == "" || !strings.Contains(raw, "+") {
		return raw
	}
	parts := strings.Split(raw, "+")
	for i := 0; i < len(parts)-1; i++ {
		if canonical, ok := shortcutModifierAliases[strings.ToLower(strings.TrimSpace(parts[i]))]; ok {
			parts[i] = canonical
			continue
		}
		parts[i] = strings.TrimSpace(parts[i])
	}
	keyPart := strings.TrimSpace(parts[len(parts)-1])
	// 单字母按键统一大写（cmd+shift+d → Cmd+Shift+D），命名键与数字保持原写作法。
	if len(keyPart) == 1 && (keyPart[0] >= 'a' && keyPart[0] <= 'z' || keyPart[0] >= 'A' && keyPart[0] <= 'Z') {
		keyPart = strings.ToUpper(keyPart)
	}
	parts[len(parts)-1] = keyPart
	return strings.Join(parts, "+")
}

// parseShortcutKey 解析单个按键名称，大小写不敏感。
func parseShortcutKey(token string) (ui.Key, error) {
	name := strings.TrimSpace(token)
	if name == "" {
		return ui.KeyUnknown, fmt.Errorf("快捷键缺少按键")
	}
	if len(name) == 1 {
		switch r := name[0]; {
		case r >= 'a' && r <= 'z':
			return ui.KeyA + ui.Key(r-'a'), nil
		case r >= 'A' && r <= 'Z':
			return ui.KeyA + ui.Key(r-'A'), nil
		case r >= '0' && r <= '9':
			return ui.Key0 + ui.Key(r-'0'), nil
		}
	}
	switch strings.ToLower(name) {
	case "comma":
		return ui.KeyComma, nil
	case "period", "dot":
		return ui.KeyPeriod, nil
	case "backquote", "backtick":
		return ui.KeyBackquote, nil
	case "slash":
		return ui.KeySlash, nil
	case "up":
		return ui.KeyUp, nil
	case "down":
		return ui.KeyDown, nil
	case "left":
		return ui.KeyLeft, nil
	case "right":
		return ui.KeyRight, nil
	case "enter", "return":
		return ui.KeyEnter, nil
	case "escape", "esc":
		return ui.KeyEscape, nil
	case "space":
		return ui.KeySpace, nil
	}
	switch name {
	case ",":
		return ui.KeyComma, nil
	case ".":
		return ui.KeyPeriod, nil
	case "`":
		return ui.KeyBackquote, nil
	case "/":
		return ui.KeySlash, nil
	case "←":
		return ui.KeyLeft, nil
	case "→":
		return ui.KeyRight, nil
	case "↑":
		return ui.KeyUp, nil
	case "↓":
		return ui.KeyDown, nil
	case "↩":
		return ui.KeyEnter, nil
	}
	return ui.KeyUnknown, fmt.Errorf("不支持的按键：%s", token)
}

// formatShortcutKeys 将修饰键与按键格式化为符号形式（如 ⇧⌘P），与列表展示一致。
func formatShortcutKeys(mods ui.Modifiers, key ui.Key) string {
	var b strings.Builder
	// 非 macOS 上 Cmd 与 Ctrl 是同一个修饰位，只输出 ⌘ 避免重复。
	if ui.Cmd != ui.Ctrl && mods&ui.Ctrl != 0 {
		b.WriteString("⌃")
	}
	if mods&ui.Alt != 0 {
		b.WriteString("⌥")
	}
	if mods&ui.Shift != 0 {
		b.WriteString("⇧")
	}
	if mods&ui.Cmd != 0 {
		b.WriteString("⌘")
	}
	b.WriteString(formatShortcutKey(key))
	return b.String()
}

// formatShortcutKey 返回单个按键的展示文案。
func formatShortcutKey(key ui.Key) string {
	switch key {
	case ui.KeyComma:
		return ","
	case ui.KeyPeriod:
		return "."
	case ui.KeyBackquote:
		return "`"
	case ui.KeySlash:
		return "/"
	case ui.KeyUp:
		return "↑"
	case ui.KeyDown:
		return "↓"
	case ui.KeyLeft:
		return "←"
	case ui.KeyRight:
		return "→"
	case ui.KeyEnter:
		return "↩"
	case ui.KeyEscape:
		return "Esc"
	case ui.KeySpace:
		return "Space"
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		return string(rune('A' + key - ui.KeyA))
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		return string(rune('0' + key - ui.Key0))
	}
	return "?"
}

type registeredShortcut struct {
	mods ui.Modifiers
	key  ui.Key
}

// dispatchActiveShortcut 遍历检测当前帧触发的应用快捷键。
func dispatchActiveShortcut(c *ui.Context) (id string, index int, ok bool) {
	bindings := effectiveShortcutBindings()
	shortcutOverridesMu.RLock()
	list := registeredShortcutsList
	shortcutOverridesMu.RUnlock()
	for _, s := range list {
		if c.Shortcut(s.mods, s.key) {
			if !ok {
				id, index, ok = matchShortcutIn(bindings, s.mods, s.key)
			}
		}
	}
	return id, index, ok
}
