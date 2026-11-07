package wire

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 标准错误码
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// 协议自定义错误码
const (
	ErrCodeUnauthorized          = -32001 // 认证失败（Token 无效/过期、未认证拦截）
	ErrCodeOperationRejected     = -32000 // 业务操作被拒绝（如状态冲突、运行中无法委派等）
	ErrCodeResourceNotFound      = -32002 // 会话不存在或项目不可访问
	ErrCodeFileNotFound          = -32004 // 文件或媒体资源不存在
	ErrCodeMediaTooLarge         = -32005 // 媒体文件超过体积上限（原图超 8MB）
	ErrCodeVersionConflict       = -32009 // CAS 版本冲突或订阅已被替代
	ErrCodeRequestAlreadyHandled = -32010 // 交互请求已被处理
	ErrCodeRequestExpired        = -32011 // 交互请求已失效
	ErrCodeRequiresHTTPS         = -32012 // isSecret 问题需要 HTTPS 入口答复
	ErrCodeTurnMismatch          = -32013 // turnID 不匹配当前执行轮次
)

// RPCError 表示 JSON-RPC 2.0 错误对象
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Request 表示 JSON-RPC 2.0 请求信封。ID 为 nil/空表示该请求为 notification。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// IsNotification 返回该请求是否为通知（无 id 字段）。
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0
}

func (r *Request) UnmarshalJSON(data []byte) error {
	type alias struct {
		JSONRPC string           `json:"jsonrpc"`
		Method  string           `json:"method"`
		Params  json.RawMessage  `json:"params"`
		ID      *json.RawMessage `json:"id"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	r.JSONRPC = a.JSONRPC
	r.Method = a.Method
	r.Params = a.Params
	if a.ID != nil {
		r.ID = *a.ID
	} else {
		r.ID = nil
	}
	return nil
}

// Response 表示 JSON-RPC 2.0 响应信封。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

func (r Response) MarshalJSON() ([]byte, error) {
	id := r.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	if r.Error != nil {
		type errResponse struct {
			JSONRPC string          `json:"jsonrpc"`
			Error   *RPCError       `json:"error"`
			ID      json.RawMessage `json:"id"`
		}
		return json.Marshal(errResponse{
			JSONRPC: "2.0",
			Error:   r.Error,
			ID:      id,
		})
	}
	type okResponse struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  any             `json:"result"`
		ID      json.RawMessage `json:"id"`
	}
	return json.Marshal(okResponse{
		JSONRPC: "2.0",
		Result:  r.Result,
		ID:      id,
	})
}
