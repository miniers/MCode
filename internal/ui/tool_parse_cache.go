package mcodeui

import "sync"

// 工具消息解析结果的进程内缓存：时间线每帧都会调用 parseCommandToolInfo 和
// toolInputFields，而它们的结果只取决于 Message 的字符串字段（正则匹配与 JSON
// 解码是主线程热点），因此按消息 ID 缓存，判定字段变化时立即失效重算。
//
// 缓存里的结果只读：调用方不得修改返回的 fields map，也不得写入
// CommandToolInfo.ExitCode 指向的值（该指针在命中时会被多个调用方共享）。
// 返回的 todo 切片同样只读，调用方不得修改其中的元素。

// toolParseCacheMaxEntries 是缓存条目数上限，超出后清空整个 map 重新开始。
const toolParseCacheMaxEntries = 4096

// toolParseCacheEntry 保存一次解析的判定字段副本与三组结果。
// hasInfo / hasFields / hasTextFields / hasTodo 标记对应结果是否已填充，各组结果可分别按需填充。
// textFields 是从 m.Text 首行提取的回退字段（ToolInput 为空的旧消息才有）。
// todoItems 是待办清单，todoOK 是「这是一条待办工具消息」的判定结果，否定结果同样缓存。
type toolParseCacheEntry struct {
	Role            string
	Kind            string
	Text            string
	ToolName        string
	ToolInput       string
	ToolStatus      string
	ToolCompletedAt string
	CreatedAt       string

	hasInfo       bool
	info          CommandToolInfo
	hasFields     bool
	fields        map[string]any
	hasTextFields bool
	textFields    map[string]any
	hasTodo       bool
	todoItems     []todoItem
	todoOK        bool
}

var (
	toolParseCacheMu sync.Mutex
	toolParseCache   = make(map[string]*toolParseCacheEntry, 256)
)

// newToolParseCacheEntry 用消息的字符串字段新建缓存条目（结果留空待填充）。
func newToolParseCacheEntry(m Message) *toolParseCacheEntry {
	return &toolParseCacheEntry{
		Role:            m.Role,
		Kind:            m.Kind,
		Text:            m.Text,
		ToolName:        m.ToolName,
		ToolInput:       m.ToolInput,
		ToolStatus:      m.ToolStatus,
		ToolCompletedAt: m.ToolCompletedAt,
		CreatedAt:       m.CreatedAt,
	}
}

// matches 判断条目记录的字段是否与消息完全一致；Go 比较共用底层数组的字符串开销很小，
// 所以不必另算哈希。
func (e *toolParseCacheEntry) matches(m Message) bool {
	return e.Role == m.Role &&
		e.Kind == m.Kind &&
		e.Text == m.Text &&
		e.ToolName == m.ToolName &&
		e.ToolInput == m.ToolInput &&
		e.ToolStatus == m.ToolStatus &&
		e.ToolCompletedAt == m.ToolCompletedAt &&
		e.CreatedAt == m.CreatedAt
}

// toolParseCacheLookup 取回与消息匹配的条目；调用方必须持有 toolParseCacheMu。
func toolParseCacheLookup(m Message) (*toolParseCacheEntry, bool) {
	e, ok := toolParseCache[m.ID]
	if !ok || !e.matches(m) {
		return nil, false
	}
	return e, true
}

// toolParseCachePut 写入条目；调用方必须持有 toolParseCacheMu。
// 已存在的同 ID 条目被覆盖，条目数超过上限时清空整个 map。
func toolParseCachePut(m Message, e *toolParseCacheEntry) {
	if _, exists := toolParseCache[m.ID]; !exists && len(toolParseCache) >= toolParseCacheMaxEntries {
		toolParseCache = make(map[string]*toolParseCacheEntry, 256)
	}
	toolParseCache[m.ID] = e
}

// toolParseCacheReset 清空缓存，仅供测试使用。
func toolParseCacheReset() {
	toolParseCacheMu.Lock()
	toolParseCache = make(map[string]*toolParseCacheEntry, 256)
	toolParseCacheMu.Unlock()
}

// toolParseCacheLen 返回当前缓存条目数，仅供测试使用。
func toolParseCacheLen() int {
	toolParseCacheMu.Lock()
	defer toolParseCacheMu.Unlock()
	return len(toolParseCache)
}
