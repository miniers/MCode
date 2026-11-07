package mcodeui

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// cachedTestMessage 构造一条带 ToolInput JSON 与多行输出文本的 bash 工具消息。
func cachedTestMessage(id string) Message {
	return Message{
		ID:        id,
		Role:      "tool",
		Kind:      "command",
		ToolName:  "bash",
		Text:      "bash {\"command\":\"ls -la\"}\ntotal 8\ndrwxr-xr-x  2 me staff 64 Jan  1 00:00 .\nexit code: 0",
		ToolInput: `{"command":"ls -la","description":"list files"}`,
		CreatedAt: "2024-01-01T00:00:00Z",
	}
}

// TestToolParseCacheStableResult 验证相同 ID 与相同内容重复调用时结果与未缓存版本一致。
func TestToolParseCacheStableResult(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestMessage("cache-stable")
	want := parseCommandToolInfoUncached(m)
	wantFields := toolInputFieldsUncached(m)
	for i := 0; i < 3; i++ {
		if got := parseCommandToolInfo(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次调用结果不一致：got %+v want %+v", i, got, want)
		}
		if got := toolInputFields(m); !reflect.DeepEqual(got, wantFields) {
			t.Fatalf("第 %d 次调用输入字段不一致：got %#v want %#v", i, got, wantFields)
		}
	}
	if n := toolParseCacheLen(); n != 1 {
		t.Fatalf("相同 ID 只应有一个缓存条目，实际 %d", n)
	}
}

// TestToolParseCacheInvalidatesOnFieldChange 验证任一判定字段变化时缓存立即失效。
func TestToolParseCacheInvalidatesOnFieldChange(t *testing.T) {
	toolParseCacheReset()

	// ToolStatus 由 running 变为 failed
	m := cachedTestMessage("cache-status")
	m.ToolStatus = "running"
	if info := parseCommandToolInfo(m); info.Failed {
		t.Fatalf("running 状态不应判定为失败：%+v", info)
	}
	m.ToolStatus = "failed"
	if info := parseCommandToolInfo(m); !info.Failed {
		t.Fatalf("failed 状态应判定为失败：%+v", info)
	}

	// ToolCompletedAt 从空变为有值，耗时随之更新
	m = cachedTestMessage("cache-duration")
	if info := parseCommandToolInfo(m); info.Duration != "" {
		t.Fatalf("未完成时耗时应为空，实际 %q", info.Duration)
	}
	m.ToolCompletedAt = "2024-01-01T00:00:02.5Z"
	if info := parseCommandToolInfo(m); info.Duration != "2.5s" {
		t.Fatalf("耗时应为 2.5s，实际 %q", info.Duration)
	}

	// Text 变化（新增 exit code 文本），ExitCode 随之更新
	m = cachedTestMessage("cache-text")
	m.Text = "bash {\"command\":\"ls -la\"}\nboom\nexit code: 3"
	info := parseCommandToolInfo(m)
	if info.ExitCode == nil || *info.ExitCode != 3 {
		t.Fatalf("ExitCode 应为 3，实际 %+v", info.ExitCode)
	}

	// ToolInput 变化，Command 与 toolInputFields 都更新
	m = cachedTestMessage("cache-input")
	if got := toolInputFields(m); got["command"] != "ls -la" {
		t.Fatalf("初始 command 应为 ls -la，实际 %v", got["command"])
	}
	m.ToolInput = `{"command":"pwd","description":"print cwd"}`
	if info := parseCommandToolInfo(m); info.Command != "pwd" {
		t.Fatalf("Command 应更新为 pwd，实际 %q", info.Command)
	}
	if got := toolInputFields(m); got["command"] != "pwd" {
		t.Fatalf("输入字段 command 应更新为 pwd，实际 %v", got["command"])
	}
}

// TestToolParseCacheSkipsEmptyID 验证 ID 为空时不写缓存且结果仍然正确。
func TestToolParseCacheSkipsEmptyID(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestMessage("")
	got := parseCommandToolInfo(m)
	if want := parseCommandToolInfoUncached(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("空 ID 结果与未缓存版本不一致：got %+v want %+v", got, want)
	}
	if got := toolInputFields(m); got["command"] != "ls -la" {
		t.Fatalf("空 ID 输入字段应正确解析，实际 %#v", got)
	}
	if n := toolParseCacheLen(); n != 0 {
		t.Fatalf("空 ID 不应写入缓存，实际条目 %d", n)
	}
}

// TestToolParseCacheEntryCap 验证写入超过上限后条目数不超过上限且结果仍然正确。
func TestToolParseCacheEntryCap(t *testing.T) {
	toolParseCacheReset()
	total := toolParseCacheMaxEntries + 8
	for i := 0; i < total; i++ {
		m := cachedTestMessage(fmt.Sprintf("cache-cap-%d", i))
		m.ToolInput = fmt.Sprintf(`{"command":"echo %d"}`, i)
		if info := parseCommandToolInfo(m); info.Command != fmt.Sprintf("echo %d", i) {
			t.Fatalf("第 %d 条命令应为 echo %d，实际 %q", i, i, info.Command)
		}
	}
	if n := toolParseCacheLen(); n > toolParseCacheMaxEntries {
		t.Fatalf("缓存条目数应不超过 %d，实际 %d", toolParseCacheMaxEntries, n)
	}
	// 清空后继续写入仍应正常工作
	m := cachedTestMessage("cache-cap-after")
	if info := parseCommandToolInfo(m); info.Command != "ls -la" {
		t.Fatalf("清空后结果应正确，实际 %q", info.Command)
	}
}

// TestToolParseCacheConcurrent 验证并发调用相同与不同 ID 时结果正确（配合 go test -race）。
func TestToolParseCacheConcurrent(t *testing.T) {
	toolParseCacheReset()
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				// 一半共享同一个 ID，另一半使用各自独立 ID
				id := "cache-concurrent-shared"
				if i%2 == 1 {
					id = fmt.Sprintf("cache-concurrent-%d", w)
				}
				m := cachedTestMessage(id)
				m.ToolInput = fmt.Sprintf(`{"command":"echo %d"}`, w)
				want := fmt.Sprintf("echo %d", w)
				if info := parseCommandToolInfo(m); info.Command != want {
					t.Errorf("worker %d 命令应为 %q，实际 %q", w, want, info.Command)
					return
				}
				if got := toolInputFields(m); got["command"] != want {
					t.Errorf("worker %d 输入字段应为 %q，实际 %v", w, want, got["command"])
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

// BenchmarkParseCommandToolInfoCached 测缓存命中路径。
func BenchmarkParseCommandToolInfoCached(b *testing.B) {
	toolParseCacheReset()
	m := cachedTestMessage("bench-cached")
	parseCommandToolInfo(m) // 预热，保证后续都是命中
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if info := parseCommandToolInfo(m); info.Command == "" {
			b.Fatal("缓存命中结果不应为空")
		}
	}
}

// BenchmarkParseCommandToolInfoUncached 测未缓存路径（正则与 JSON 解析）。
func BenchmarkParseCommandToolInfoUncached(b *testing.B) {
	m := cachedTestMessage("bench-uncached")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if info := parseCommandToolInfoUncached(m); info.Command == "" {
			b.Fatal("未缓存结果不应为空")
		}
	}
}

// cachedTestLegacyMessage 构造一条 ToolInput 为空、输入 JSON 只留在文本首行的旧工具消息。
func cachedTestLegacyMessage(id, text string) Message {
	return Message{
		ID:        id,
		Role:      "tool",
		Kind:      "command",
		ToolName:  "bash",
		Text:      text,
		CreatedAt: "2024-01-01T00:00:00Z",
	}
}

// toolParseCacheTextFieldsState 返回指定条目上文本回退字段的缓存状态，仅供测试使用。
func toolParseCacheTextFieldsState(id string) (map[string]any, bool) {
	toolParseCacheMu.Lock()
	defer toolParseCacheMu.Unlock()
	e, ok := toolParseCache[id]
	if !ok {
		return nil, false
	}
	return e.textFields, e.hasTextFields
}

// TestToolCallFieldsTextFallbackCached 验证旧消息（ToolInput 为空）的文本回退字段被缓存复用。
func TestToolCallFieldsTextFallbackCached(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestLegacyMessage("cache-text-fallback", "bash {\"command\":\"ls\"}\ntotal 0")
	want := toolDisplayFieldsFromText(m.Text)
	first := toolCallFields(m)
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("首次调用结果不一致：got %#v want %#v", first, want)
	}
	second := toolCallFields(m)
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("再次调用结果不一致：got %#v want %#v", second, want)
	}
	if fmt.Sprintf("%p", first) != fmt.Sprintf("%p", second) {
		t.Fatalf("内容未变时应返回同一个 map，避免重复解析")
	}
	if _, has := toolParseCacheTextFieldsState(m.ID); !has {
		t.Fatalf("缓存条目上应标记 hasTextFields")
	}
	if n := toolParseCacheLen(); n != 1 {
		t.Fatalf("相同 ID 只应有一个缓存条目，实际 %d", n)
	}
}

// TestToolCallFieldsTextFallbackInvalidatesOnTextChange 验证相同 ID 下文本变化时回退字段立即更新。
func TestToolCallFieldsTextFallbackInvalidatesOnTextChange(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestLegacyMessage("cache-text-change", "bash {\"command\":\"ls\"}")
	if got := toolCallFields(m); got["command"] != "ls" {
		t.Fatalf("首次命令应为 ls，实际 %v", got["command"])
	}
	m.Text = "bash {\"command\":\"pwd\"}"
	if got := toolCallFields(m); got["command"] != "pwd" {
		t.Fatalf("文本变化后命令应为 pwd，实际 %v", got["command"])
	}
}

// TestToolCallFieldsPrefersToolInputWhenAdded 验证相同 ID 补上 ToolInput 后改用 ToolInput 的解析结果。
func TestToolCallFieldsPrefersToolInputWhenAdded(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestLegacyMessage("cache-text-to-input", "bash {\"command\":\"ls\"}")
	if got := toolCallFields(m); got["command"] != "ls" {
		t.Fatalf("回退解析命令应为 ls，实际 %v", got["command"])
	}
	m.ToolInput = `{"command":"ls -la","description":"detail"}`
	want := toolInputFieldsUncached(m)
	got := toolCallFields(m)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("补上 ToolInput 后应返回其解析结果：got %#v want %#v", got, want)
	}
	if got["command"] != "ls -la" {
		t.Fatalf("ToolInput 的命令应生效，实际 %v", got["command"])
	}
}

// TestToolCallFieldsTextFallbackNoCacheForEmptyID 验证 ID 为空时不写缓存且结果正确。
func TestToolCallFieldsTextFallbackNoCacheForEmptyID(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestLegacyMessage("", "bash {\"command\":\"ls\"}")
	want := toolDisplayFieldsFromText(m.Text)
	for i := 0; i < 2; i++ {
		if got := toolCallFields(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次调用结果不一致：got %#v want %#v", i, got, want)
		}
	}
	if n := toolParseCacheLen(); n != 0 {
		t.Fatalf("ID 为空时不应写缓存，实际条目数 %d", n)
	}
}

// todoCacheEntry 取回与消息匹配的缓存条目；不存在或字段失配时返回 nil。
func todoCacheEntry(m Message) *toolParseCacheEntry {
	toolParseCacheMu.Lock()
	defer toolParseCacheMu.Unlock()
	e, ok := toolParseCacheLookup(m)
	if !ok {
		return nil
	}
	return e
}

// todoInputMessage 构造一条待办工具消息（ToolInput 里是 todos 数组）。
func todoInputMessage(id, toolInput string) Message {
	return Message{
		ID:        id,
		Role:      "tool",
		Kind:      "tool",
		ToolName:  "TodoWrite",
		ToolInput: toolInput,
		CreatedAt: "2024-01-01T00:00:00Z",
	}
}

// TestTodoItemsFromMessageCachesToolInput 验证 ToolInput 含 todos 时结果正确，且内容不变时第二次命中缓存。
func TestTodoItemsFromMessageCachesToolInput(t *testing.T) {
	toolParseCacheReset()
	m := todoInputMessage("todo-input", `{"todos":[{"content":"写测试","status":"completed"},{"content":"跑验证","status":"in_progress"}]}`)

	items, ok := todoItemsFromMessage(m)
	if !ok || len(items) != 2 {
		t.Fatalf("应解析出 2 项待办，实际 ok=%v items=%+v", ok, items)
	}
	if items[0].Text != "写测试" || items[0].Status != "completed" {
		t.Fatalf("第 1 项解析错误：%+v", items[0])
	}
	if items[1].Text != "跑验证" || items[1].Status != "in_progress" {
		t.Fatalf("第 2 项解析错误：%+v", items[1])
	}
	e := todoCacheEntry(m)
	if e == nil || !e.hasTodo || !e.todoOK {
		t.Fatalf("解析结果应写入缓存：%+v", e)
	}

	// 改写条目里的缓存值：第二次调用若直接返回它，说明没有再解析一次 JSON。
	toolParseCacheMu.Lock()
	e.todoItems = []todoItem{{Text: "缓存命中", Status: "pending"}}
	toolParseCacheMu.Unlock()
	items, ok = todoItemsFromMessage(m)
	if !ok || len(items) != 1 || items[0].Text != "缓存命中" {
		t.Fatalf("第二次调用应命中缓存，实际 ok=%v items=%+v", ok, items)
	}
}

// TestTodoItemsFromMessageTextFallback 验证 ToolInput 解析失败时回退到 Text 里的 plan。
func TestTodoItemsFromMessageTextFallback(t *testing.T) {
	toolParseCacheReset()
	m := Message{
		ID:        "todo-fallback",
		Role:      "tool",
		Kind:      "tool",
		ToolName:  "update_plan",
		ToolInput: `{"plan":`,
		Text:      `update_plan {"plan":[{"step":"跑测试","status":"pending"}]}`,
	}
	items, ok := todoItemsFromMessage(m)
	if !ok || len(items) != 1 || items[0].Text != "跑测试" {
		t.Fatalf("应回退到 Text 解析出 1 项待办，实际 ok=%v items=%+v", ok, items)
	}
	if items[0].Status != "pending" {
		t.Fatalf("状态应为 pending，实际 %q", items[0].Status)
	}
}

// TestTodoItemsFromMessageCachesNegative 验证非待办消息返回 false，且否定结果同样被缓存。
func TestTodoItemsFromMessageCachesNegative(t *testing.T) {
	toolParseCacheReset()
	m := cachedTestMessage("todo-negative")
	if items, ok := todoItemsFromMessage(m); ok || len(items) != 0 {
		t.Fatalf("普通工具消息不应判定为待办：ok=%v items=%+v", ok, items)
	}
	e := todoCacheEntry(m)
	if e == nil || !e.hasTodo || e.todoOK {
		t.Fatalf("否定结果应写入缓存：%+v", e)
	}

	// 改写条目里的缓存值：第二次调用若直接返回它，说明没有再解析一次 JSON。
	toolParseCacheMu.Lock()
	e.todoItems, e.todoOK = []todoItem{{Text: "否定缓存命中", Status: "pending"}}, true
	toolParseCacheMu.Unlock()
	items, ok := todoItemsFromMessage(m)
	if !ok || len(items) != 1 || items[0].Text != "否定缓存命中" {
		t.Fatalf("第二次调用应命中否定缓存，实际 ok=%v items=%+v", ok, items)
	}
}

// TestTodoItemsFromMessageInvalidatesOnFieldChange 验证同一 ID 下 Text 或 ToolInput 变化时结果立即更新。
func TestTodoItemsFromMessageInvalidatesOnFieldChange(t *testing.T) {
	toolParseCacheReset()

	// ToolInput 变化
	m := todoInputMessage("todo-change-input", `{"todos":[{"content":"第一版","status":"pending"}]}`)
	if items, ok := todoItemsFromMessage(m); !ok || items[0].Text != "第一版" {
		t.Fatalf("初始待办应为第一版，实际 ok=%v items=%+v", ok, items)
	}
	m.ToolInput = `{"todos":[{"content":"第二版","status":"completed"}]}`
	items, ok := todoItemsFromMessage(m)
	if !ok || len(items) != 1 || items[0].Text != "第二版" || items[0].Status != "completed" {
		t.Fatalf("ToolInput 变化后应更新为第二版，实际 ok=%v items=%+v", ok, items)
	}

	// 走 Text 回退时 Text 变化：ToolInput 未变也不能沿用旧结果
	legacy := Message{
		ID:        "todo-change-text",
		Role:      "tool",
		Kind:      "tool",
		ToolName:  "update_plan",
		Text:      `update_plan {"plan":[{"step":"旧步骤","status":"pending"}]}`,
		ToolInput: `{"plan":`,
	}
	if got, ok := todoItemsFromMessage(legacy); !ok || got[0].Text != "旧步骤" {
		t.Fatalf("回退待办应为旧步骤，实际 ok=%v items=%+v", ok, got)
	}
	legacy.Text = `update_plan {"plan":[{"step":"新步骤","status":"completed"}]}`
	got, ok := todoItemsFromMessage(legacy)
	if !ok || len(got) != 1 || got[0].Text != "新步骤" || got[0].Status != "completed" {
		t.Fatalf("Text 变化后应更新为新步骤，实际 ok=%v items=%+v", ok, got)
	}

	// ToolInput 含 todos 时 Text 变化（内容里追加了输出）仍需返回原待办
	m = todoInputMessage("todo-change-text-with-input", `{"todos":[{"content":"保持","status":"pending"}]}`)
	if got, ok := todoItemsFromMessage(m); !ok || got[0].Text != "保持" {
		t.Fatalf("Text 为空时待办应为保持，实际 ok=%v items=%+v", ok, got)
	}
	m.Text = "TodoWrite 已完成\n输出若干行"
	got, ok = todoItemsFromMessage(m)
	if !ok || len(got) != 1 || got[0].Text != "保持" {
		t.Fatalf("Text 变化后待办仍应为保持，实际 ok=%v items=%+v", ok, got)
	}
}

// TestTodoItemsFromMessageSkipsEmptyID 验证 ID 为空时结果正确且不写缓存。
func TestTodoItemsFromMessageSkipsEmptyID(t *testing.T) {
	toolParseCacheReset()
	m := todoInputMessage("", `{"todos":[{"content":"无 ID","status":"in_progress"}]}`)
	for i := 0; i < 2; i++ {
		items, ok := todoItemsFromMessage(m)
		if !ok || len(items) != 1 || items[0].Text != "无 ID" || items[0].Status != "in_progress" {
			t.Fatalf("第 %d 次调用结果错误：ok=%v items=%+v", i, ok, items)
		}
	}
	if n := toolParseCacheLen(); n != 0 {
		t.Fatalf("ID 为空时不应写缓存，实际条目数 %d", n)
	}
}
