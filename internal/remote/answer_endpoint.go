package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"mcode/internal/remote/wire"
)

// maxAnswerBodyBytes 是作答请求体的上限：只含三个短字段，4 KiB 足够且能挡住误发的整页数据。
const maxAnswerBodyBytes = 4 << 10

// AnswerBackend 是作答端点依赖的最小后端面：读当前待处理快照并提交答复。
type AnswerBackend interface {
	ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error)
	RespondRequest(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error)
}

// AnswerEndpointOptions 提供作答端点的配置项。
type AnswerEndpointOptions struct {
	Auth    *PairingAuth
	Backend AnswerBackend
	Entry   Entry
}

// answerRequest 是 Service Worker 提交的最小载荷：目标由服务端按当前快照补齐。
type answerRequest struct {
	ThreadID  string `json:"threadID"`
	RequestID string `json:"requestID"`
	Choice    string `json:"choice"`
}

type answerEndpoint struct {
	auth    *PairingAuth
	backend AnswerBackend
	entry   Entry
}

// NewAnswerEndpoint 返回推送通知内联作答的 HTTP 处理器：POST /api/remote/answer。
// 只接受会话令牌与既有选项原文，不开放自由文本，也不改变 requests.respond 的任何规则。
func NewAnswerEndpoint(opts AnswerEndpointOptions) http.Handler {
	return &answerEndpoint{auth: opts.Auth, backend: opts.Backend, entry: opts.Entry}
}

func (h *answerEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAnswerJSON(w, http.StatusMethodNotAllowed, answerErrorBody("method not allowed"))
		return
	}
	// 纵深防御：路由层已按入口拒绝 LAN，这里再按处理器自己的入口判一次。
	if !h.entry.Secure() {
		writeAnswerJSON(w, http.StatusForbidden, answerErrorBody("answers require the HTTPS entry"))
		return
	}
	if h.auth == nil || h.backend == nil {
		writeAnswerJSON(w, http.StatusInternalServerError, answerErrorBody("answer endpoint unavailable"))
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAnswerJSON(w, http.StatusUnsupportedMediaType, answerErrorBody("unsupported media type"))
		return
	}

	limited := http.MaxBytesReader(w, r.Body, maxAnswerBodyBytes)
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	var body answerRequest
	if err := dec.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAnswerJSON(w, http.StatusRequestEntityTooLarge, answerErrorBody("request body too large"))
			return
		}
		writeAnswerJSON(w, http.StatusBadRequest, answerErrorBody("invalid request body"))
		return
	}
	// 只接受单个 JSON 对象：尾随数据一律视为坏请求。
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAnswerJSON(w, http.StatusBadRequest, answerErrorBody("invalid request body"))
		return
	}

	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeAnswerJSON(w, http.StatusUnauthorized, answerErrorBody("unauthorized"))
		return
	}
	// 鉴权只用于确认设备身份，不记录令牌本身。
	if _, _, err := h.auth.Authenticate(token, ClientInfo{
		Entry:     h.entry,
		UserAgent: r.UserAgent(),
		Address:   extractClientIP(r.RemoteAddr),
	}); err != nil {
		writeAnswerJSON(w, http.StatusUnauthorized, answerErrorBody("unauthorized"))
		return
	}

	if !isValidUUID(body.ThreadID) {
		writeAnswerJSON(w, http.StatusBadRequest, answerErrorBody("invalid threadID"))
		return
	}
	if strings.TrimSpace(body.RequestID) == "" {
		writeAnswerJSON(w, http.StatusBadRequest, answerErrorBody("invalid requestID"))
		return
	}

	list, err := h.backend.ListRequests(r.Context(), body.ThreadID)
	if err != nil {
		var rpcErr *wire.RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == wire.ErrCodeResourceNotFound {
			writeAnswerJSON(w, http.StatusNotFound, answerErrorBody("thread not found"))
			return
		}
		writeAnswerJSON(w, http.StatusInternalServerError, answerErrorBody("internal error"))
		return
	}
	var targetReq *wire.RemoteRequest
	for i := range list.Requests {
		if list.Requests[i].RequestID == body.RequestID {
			targetReq = &list.Requests[i]
			break
		}
	}
	if targetReq == nil {
		// 重复点击与另一台设备已处理都会落到这里：请求已不在待处理列表。
		writeAnswerJSON(w, http.StatusConflict, answerErrorBody("request is no longer pending"))
		return
	}

	target := answerTargetFor(*targetReq)
	if target == nil {
		writeAnswerJSON(w, http.StatusForbidden, answerErrorBody("request is not answerable inline"))
		return
	}
	matched := false
	for _, option := range target.Answers {
		if option.Value == body.Choice {
			matched = true
			break
		}
	}
	if !matched {
		writeAnswerJSON(w, http.StatusBadRequest, answerErrorBody("invalid choice"))
		return
	}

	answers, err := json.Marshal(map[string][]string{target.QuestionID: {body.Choice}})
	if err != nil {
		writeAnswerJSON(w, http.StatusInternalServerError, answerErrorBody("internal error"))
		return
	}
	// 通知作答来自远程设备，计分时记为 remote，而不是桌面直接答的 direct。
	ctx := WithAnswerSource(r.Context(), AnswerSourceRemote)
	// runID / turnID 取自服务端当前快照，客户端无法伪造作答目标。
	if _, err := h.backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  body.ThreadID,
		TurnID:    targetReq.TurnID,
		RunID:     targetReq.RunID,
		RequestID: targetReq.RequestID,
		Answers:   answers,
	}); err != nil {
		writeAnswerJSON(w, answerStatusForError(err), answerErrorBody(answerMessageForError(err)))
		return
	}
	writeAnswerJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// bearerToken 从 Authorization 头取出 Bearer 令牌；前缀或令牌不符合时返回 false。
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != ""
}

// answerStatusForError 把 RespondRequest 的错误码映射为 HTTP 状态码。
func answerStatusForError(err error) int {
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		return http.StatusInternalServerError
	}
	switch rpcErr.Code {
	case wire.ErrCodeRequestAlreadyHandled, wire.ErrCodeRequestExpired, wire.ErrCodeOperationRejected:
		return http.StatusConflict
	case wire.InvalidParams:
		return http.StatusBadRequest
	case wire.ErrCodeResourceNotFound:
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// answerMessageForError 给常见错误码返回稳定文案；其余不回显后端细节。
func answerMessageForError(err error) string {
	switch answerStatusForError(err) {
	case http.StatusConflict:
		return "request is no longer pending"
	case http.StatusBadRequest:
		return "invalid answer"
	case http.StatusNotFound:
		return "thread not found"
	}
	return "internal error"
}

func answerErrorBody(message string) map[string]string {
	return map[string]string{"error": message}
}

// writeAnswerJSON 统一作答端点的响应头与 JSON 编码：这些响应都不允许缓存。
func writeAnswerJSON(w http.ResponseWriter, status int, payload any) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
