package wire

import "encoding/json"

// 远程设置读写（settings.get / settings.set）的协议类型。
// values 用 map[string]json.RawMessage：键集合由 internal/remote/settings_ext_methods.go 的白名单
// （SettingsWritableKeys）固定，每个值的类型由 docs/remote-control.schema.json 描述，
// Go 侧不重复声明 25 个字段类型，避免两边各自漂移。

// SettingsSnapshot 是 settings.get 与 settings.set 的共同结果：
// 值快照和白名单一起返回，客户端据此渲染表单，不必把白名单硬编码在前端。
type SettingsSnapshot struct {
	// Values 是白名单每个键的当前值，键总是齐全；未设置的指针字段给出生效值。
	Values map[string]json.RawMessage `json:"values"`
	// WritableKeys 是 settings.set 接受的键，顺序固定，只读键不在其中。
	WritableKeys []string `json:"writableKeys"`
	// QuickReplies 是输入框上方的快捷回复，只读，不进白名单。
	QuickReplies []SettingsQuickReply `json:"quickReplies"`
}

// SettingsGetParams 是 settings.get 的参数：目前没有字段，客户端传 {}。
// 保留具名类型是为了以后加分页或字段过滤时不改方法签名。
type SettingsGetParams struct{}

// SettingsSetParams 是 settings.set 的参数。
type SettingsSetParams struct {
	// Values 是要写入的键值；任一键不在白名单或值非法时整体拒绝，不部分生效。
	Values map[string]json.RawMessage `json:"values"`
	// ActionID 是客户端的幂等键，当前版本只原样接收，不做去重。
	ActionID string `json:"actionID,omitempty"`
}

// SettingsQuickReply 是输入框上方的一枚快捷回复。
type SettingsQuickReply struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

// SettingsChangedNotification 是 settings.changed 的载荷：只带变化的键，
// 客户端据此就地更新表单，不必重新拉一次 settings.get。
type SettingsChangedNotification struct {
	Values map[string]json.RawMessage `json:"values"`
}
