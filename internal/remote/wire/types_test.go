package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestJSONRPCRoundTrip(t *testing.T) {
	// 1. Request 解析：notification 无 id
	notifJSON := `{"jsonrpc":"2.0","method":"threads.event","params":{"threadID":"00000000-0000-0000-0000-000000000001"}}`
	var notifReq Request
	if err := json.Unmarshal([]byte(notifJSON), &notifReq); err != nil {
		t.Fatalf("unmarshal notif error: %v", err)
	}
	if !notifReq.IsNotification() {
		t.Errorf("expected notification, got ID: %s", string(notifReq.ID))
	}

	// 2. Request 解析：带整数 id
	reqJSON := `{"jsonrpc":"2.0","method":"projects.list","params":{},"id":42}`
	var req Request
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		t.Fatalf("unmarshal req error: %v", err)
	}
	if req.IsNotification() {
		t.Errorf("expected request, got notification")
	}
	if string(req.ID) != "42" {
		t.Errorf("expected ID 42, got %s", string(req.ID))
	}

	// 3. Response 序列化：成功响应不包含 error 字段
	okResp := Response{
		JSONRPC: "2.0",
		Result: map[string]any{
			"status": "ok",
		},
		ID: json.RawMessage("1"),
	}
	okBytes, err := json.Marshal(okResp)
	if err != nil {
		t.Fatalf("marshal ok response error: %v", err)
	}
	var okMap map[string]any
	if err := json.Unmarshal(okBytes, &okMap); err != nil {
		t.Fatalf("unmarshal ok map error: %v", err)
	}
	if _, hasErr := okMap["error"]; hasErr {
		t.Errorf("success response should not contain 'error' field")
	}
	if okMap["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", okMap["jsonrpc"])
	}

	// 4. Response 序列化：错误响应不包含 result 字段
	errResp := Response{
		JSONRPC: "2.0",
		Error: &RPCError{
			Code:    ErrCodeUnauthorized,
			Message: "Unauthorized",
		},
		ID: json.RawMessage("2"),
	}
	errBytes, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("marshal err response error: %v", err)
	}
	var errMap map[string]any
	if err := json.Unmarshal(errBytes, &errMap); err != nil {
		t.Fatalf("unmarshal err map error: %v", err)
	}
	if _, hasResult := errMap["result"]; hasResult {
		t.Errorf("error response should not contain 'result' field")
	}
	errObj, ok := errMap["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", errMap["error"])
	}
	if int(errObj["code"].(float64)) != ErrCodeUnauthorized {
		t.Errorf("expected error code %d, got %v", ErrCodeUnauthorized, errObj["code"])
	}
}

func TestThreadMessageTurnTimingJSON(t *testing.T) {
	started, completed, status := "2026-11-06T01:02:03.004Z", "2026-11-06T01:02:04.005Z", "completed"
	msg := ThreadMessage{ID: "m1", Role: "user", CreatedAt: started, TurnIndex: 0, TurnStatus: &status, TurnStartedAt: &started, TurnCompletedAt: &completed}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["turnStatus"] != status || got["turnStartedAt"] != started || got["turnCompletedAt"] != completed {
		t.Fatalf("turn timing JSON mismatch: %s", data)
	}
	var empty ThreadMessage
	data, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "turnStartedAt") || strings.Contains(string(data), "turnCompletedAt") {
		t.Fatalf("zero timing fields must be omitted: %s", data)
	}
}

func TestSessionTypesRoundTrip(t *testing.T) {
	// session.pair 结果往返
	pairResultJSON := `{
		"sessionToken": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"deviceID": "95a01908-6b43-40be-8d84-057bb3fa6826",
		"label": "iPhone 16 Pro"
	}`
	var pairRes SessionPairResult
	if err := json.Unmarshal([]byte(pairResultJSON), &pairRes); err != nil {
		t.Fatalf("unmarshal SessionPairResult: %v", err)
	}
	if pairRes.DeviceID != "95a01908-6b43-40be-8d84-057bb3fa6826" || pairRes.Label != "iPhone 16 Pro" {
		t.Errorf("unexpected SessionPairResult: %+v", pairRes)
	}
	out, err := json.Marshal(pairRes)
	if err != nil {
		t.Fatalf("marshal SessionPairResult: %v", err)
	}
	var check map[string]any
	if err := json.Unmarshal(out, &check); err != nil {
		t.Fatalf("unmarshal check map: %v", err)
	}
	if check["sessionToken"] != pairRes.SessionToken || check["deviceID"] != pairRes.DeviceID {
		t.Errorf("mismatch in SessionPairResult fields: %v", check)
	}

	// session.authenticate 结果往返
	authResultJSON := `{
		"authenticated": true,
		"deviceID": "95a01908-6b43-40be-8d84-057bb3fa6826",
		"label": "iPhone 16 Pro"
	}`
	var authRes SessionAuthenticateResult
	if err := json.Unmarshal([]byte(authResultJSON), &authRes); err != nil {
		t.Fatalf("unmarshal SessionAuthenticateResult: %v", err)
	}
	if !authRes.Authenticated || authRes.DeviceID != "95a01908-6b43-40be-8d84-057bb3fa6826" {
		t.Errorf("unexpected SessionAuthenticateResult: %+v", authRes)
	}

	// session.newPairingCode 结果往返
	newPairingJSON := `{
		"requested": true,
		"pairingURL": "https://example.com:8443/#pair=abcdef",
		"lanPairingURL": "http://192.168.1.100:43117/#pair=abcdef",
		"expiresAt": "2026-10-16T08:05:00Z"
	}`
	var newPairingRes SessionNewPairingCodeResult
	if err := json.Unmarshal([]byte(newPairingJSON), &newPairingRes); err != nil {
		t.Fatalf("unmarshal SessionNewPairingCodeResult: %v", err)
	}
	if !newPairingRes.Requested || newPairingRes.LANPairingURL != "http://192.168.1.100:43117/#pair=abcdef" {
		t.Errorf("unexpected SessionNewPairingCodeResult: %+v", newPairingRes)
	}
}

func TestCapabilitiesRoundTrip(t *testing.T) {
	capJSON := `{
		"schemaVersion": 1,
		"methods": ["session.pair", "capabilities.get"],
		"features": ["threadHistoryPaging", "threads.events"],
		"webBuildID": "1.0.0"
	}`
	var caps Capabilities
	if err := json.Unmarshal([]byte(capJSON), &caps); err != nil {
		t.Fatalf("unmarshal Capabilities: %v", err)
	}
	if caps.SchemaVersion != 1 || len(caps.Methods) != 2 || len(caps.Features) != 2 || caps.WebBuildID != "1.0.0" {
		t.Errorf("unexpected Capabilities: %+v", caps)
	}
}

func TestKanbanBoardRoundTrip(t *testing.T) {
	boardJSON := `{
		"schemaVersion": 1,
		"revision": 3,
		"projects": [{"id": "11111111-1111-1111-1111-111111111111", "name": "mcode"}],
		"tasks": [],
		"goals": []
	}`
	var board KanbanBoard
	if err := json.Unmarshal([]byte(boardJSON), &board); err != nil {
		t.Fatalf("unmarshal KanbanBoard: %v", err)
	}
	if board.SchemaVersion != 1 || board.Revision != 3 || len(board.Projects) != 1 || len(board.Tasks) != 0 {
		t.Errorf("unexpected KanbanBoard: %+v", board)
	}
	if board.Projects[0].Name != "mcode" {
		t.Errorf("unexpected project name: %s", board.Projects[0].Name)
	}
}

func TestProjectsAndThreadsRoundTrip(t *testing.T) {
	// projects.list 结果往返
	projListJSON := `{
		"projects": [
			{"id": "00000000-0000-0000-0000-000000000001", "name": "demo", "path": "/path/to/demo", "isExpanded": true, "threadCount": 5}
		],
		"revision": 1
	}`
	var plist ProjectList
	if err := json.Unmarshal([]byte(projListJSON), &plist); err != nil {
		t.Fatalf("unmarshal ProjectList: %v", err)
	}
	if plist.Revision != 1 || len(plist.Projects) != 1 || plist.Projects[0].ThreadCount != 5 {
		t.Errorf("unexpected ProjectList: %+v", plist)
	}

	// threads.list 结果往返
	threadListJSON := `{
		"threads": [
			{
				"id": "22222222-2222-2222-2222-222222222222",
				"projectID": "00000000-0000-0000-0000-000000000001",
				"title": "测试会话",
				"provider": "claude",
				"model": "claude-3-5-sonnet",
				"effort": null,
				"isRunning": false,
				"isArchived": false,
				"isPinned": true,
				"isDormant": false,
				"hasUnread": false,
				"attention": "none",
				"lastActivityText": "AI已回复",
				"section": "pinned",
				"orderKey": "001",
				"updatedAt": "2026-10-16T08:00:00Z",
				"lastStatus": "completed",
				"hasPendingRequest": false,
				"taskID": null,
				"goalID": null,
				"runtimeMode": "supervised",
				"interactionMode": "build",
				"fastMode": false,
				"worktreeRequested": false,
				"swarmEnabled": false,
				"swarmPairID": null,
				"swarmBeesProvider": null,
				"swarmBeesModel": null,
				"swarmBeesEffort": null
			}
		],
		"revision": 10,
		"truncated": false,
		"truncationReason": null,
		"nextCursor": null,
		"hasMore": false
	}`
	var tlist ThreadList
	if err := json.Unmarshal([]byte(threadListJSON), &tlist); err != nil {
		t.Fatalf("unmarshal ThreadList: %v", err)
	}
	if len(tlist.Threads) != 1 || tlist.Revision != 10 || tlist.Threads[0].Title != "测试会话" {
		t.Errorf("unexpected ThreadList: %+v", tlist)
	}
	if tlist.Threads[0].Model == nil || *tlist.Threads[0].Model != "claude-3-5-sonnet" {
		t.Errorf("unexpected Model in ThreadSummary")
	}

	// threads.history 页面往返
	historyJSON := `{
		"threadID": "22222222-2222-2222-2222-222222222222",
		"messages": [
			{
				"id": "33333333-3333-3333-3333-333333333333",
				"role": "user",
				"text": "Hello world",
				"createdAt": "2026-10-16T08:01:00Z",
				"turnIndex": 1
			}
		],
		"nextCursor": null,
		"hasMore": false,
		"truncated": false
	}`
	var hpage HistoryPage
	if err := json.Unmarshal([]byte(historyJSON), &hpage); err != nil {
		t.Fatalf("unmarshal HistoryPage: %v", err)
	}
	if len(hpage.Messages) != 1 || hpage.Messages[0].Text != "Hello world" {
		t.Errorf("unexpected HistoryPage: %+v", hpage)
	}
}

func TestNotificationsRoundTrip(t *testing.T) {
	// threads.event 通知往返
	eventJSON := `{
		"threadID": "22222222-2222-2222-2222-222222222222",
		"runID": "idle:22222222-2222-2222-2222-222222222222",
		"sequence": 1,
		"kind": "projection",
		"payload": {
			"messages": [],
			"removedMessageIDs": [],
			"thread": {
				"id": "22222222-2222-2222-2222-222222222222",
				"projectID": "00000000-0000-0000-0000-000000000001",
				"title": "测试会话",
				"provider": "claude",
				"model": null,
				"effort": null,
				"isRunning": false,
				"isArchived": false,
				"isPinned": false,
				"isDormant": false,
				"hasUnread": false,
				"attention": "none",
				"lastActivityText": null,
				"section": "active",
				"orderKey": "002",
				"updatedAt": "2026-10-16T08:00:00Z",
				"lastStatus": null,
				"hasPendingRequest": false,
				"taskID": null,
				"goalID": null,
				"runtimeMode": "supervised",
				"interactionMode": "build",
				"fastMode": false,
				"worktreeRequested": false,
				"swarmEnabled": false,
				"swarmPairID": null,
				"swarmBeesProvider": null,
				"swarmBeesModel": null,
				"swarmBeesEffort": null
			}
		}
	}`
	var evt ThreadEvent
	if err := json.Unmarshal([]byte(eventJSON), &evt); err != nil {
		t.Fatalf("unmarshal ThreadEvent: %v", err)
	}
	if evt.Sequence != 1 || evt.Kind != "projection" || evt.Payload.Thread.Title != "测试会话" {
		t.Errorf("unexpected ThreadEvent: %+v", evt)
	}

	// threads.changed 通知往返
	changedJSON := `{
		"revision": 15,
		"threads": [],
		"removedThreadIDs": ["22222222-2222-2222-2222-222222222222"]
	}`
	var tc ThreadsChanged
	if err := json.Unmarshal([]byte(changedJSON), &tc); err != nil {
		t.Fatalf("unmarshal ThreadsChanged: %v", err)
	}
	if tc.Revision != 15 || len(tc.RemovedThreadIDs) != 1 {
		t.Errorf("unexpected ThreadsChanged: %+v", tc)
	}
}

func TestThreadCreateTypesRoundTrip(t *testing.T) {
	// 1. ThreadCreateParams 序列化往返（带 Options）
	paramsWithOptsJSON := `{
		"projectID": "11111111-1111-1111-1111-111111111111",
		"actionID": "22222222-2222-2222-2222-222222222222",
		"options": {"provider":"claude"}
	}`
	var params ThreadCreateParams
	if err := json.Unmarshal([]byte(paramsWithOptsJSON), &params); err != nil {
		t.Fatalf("unmarshal ThreadCreateParams: %v", err)
	}
	if params.ProjectID != "11111111-1111-1111-1111-111111111111" || params.ActionID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("unexpected ThreadCreateParams: %+v", params)
	}
	out, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal ThreadCreateParams: %v", err)
	}
	var checkMap map[string]any
	if err := json.Unmarshal(out, &checkMap); err != nil {
		t.Fatalf("unmarshal check map: %v", err)
	}
	if checkMap["projectID"] != "11111111-1111-1111-1111-111111111111" || checkMap["actionID"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("fields mismatch: %v", checkMap)
	}
	if _, ok := checkMap["options"]; !ok {
		t.Errorf("expected options to be present")
	}

	// 2. ThreadCreateParams 当 Options 为空时不输出
	paramsEmptyOpts := ThreadCreateParams{
		ProjectID: "11111111-1111-1111-1111-111111111111",
		ActionID:  "22222222-2222-2222-2222-222222222222",
	}
	outEmptyOpts, err := json.Marshal(paramsEmptyOpts)
	if err != nil {
		t.Fatalf("marshal ThreadCreateParams without options: %v", err)
	}
	var checkEmptyMap map[string]any
	if err := json.Unmarshal(outEmptyOpts, &checkEmptyMap); err != nil {
		t.Fatalf("unmarshal empty options map: %v", err)
	}
	if _, ok := checkEmptyMap["options"]; ok {
		t.Errorf("options should not be emitted when empty, got: %v", checkEmptyMap)
	}
	if checkEmptyMap["projectID"] != "11111111-1111-1111-1111-111111111111" || checkEmptyMap["actionID"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("fields mismatch: %v", checkEmptyMap)
	}

	// 3. ThreadCreateResult 序列化往返
	resultJSON := `{
		"actionID": "22222222-2222-2222-2222-222222222222",
		"threadID": "33333333-3333-3333-3333-333333333333"
	}`
	var res ThreadCreateResult
	if err := json.Unmarshal([]byte(resultJSON), &res); err != nil {
		t.Fatalf("unmarshal ThreadCreateResult: %v", err)
	}
	if res.ActionID != "22222222-2222-2222-2222-222222222222" || res.ThreadID != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("unexpected ThreadCreateResult: %+v", res)
	}
	outRes, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal ThreadCreateResult: %v", err)
	}
	var checkResMap map[string]any
	if err := json.Unmarshal(outRes, &checkResMap); err != nil {
		t.Fatalf("unmarshal check res map: %v", err)
	}
	if checkResMap["actionID"] != "22222222-2222-2222-2222-222222222222" || checkResMap["threadID"] != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("fields mismatch in ThreadCreateResult: %v", checkResMap)
	}

	// 4. ThreadCreateParams 携带可选的 actionJournalID
	actionJournalID := "aj-12345"
	paramsWithAJ := ThreadCreateParams{
		ProjectID:       "11111111-1111-1111-1111-111111111111",
		ActionID:        "22222222-2222-2222-2222-222222222222",
		ActionJournalID: &actionJournalID,
	}
	outAJ, err := json.Marshal(paramsWithAJ)
	if err != nil {
		t.Fatalf("marshal ThreadCreateParams with ActionJournalID: %v", err)
	}
	var checkAJMap map[string]any
	if err := json.Unmarshal(outAJ, &checkAJMap); err != nil {
		t.Fatalf("unmarshal check aj map: %v", err)
	}
	if checkAJMap["actionJournalID"] != "aj-12345" {
		t.Errorf("expected actionJournalID aj-12345, got %v", checkAJMap["actionJournalID"])
	}
}

func TestRequestsTypesRoundTrip(t *testing.T) {
	// 1. RemoteRequest 往返
	turnID := "11111111-1111-1111-1111-111111111111"
	title := "bash"
	detail := "echo hello"
	req := RemoteRequest{
		ThreadID:  "00000000-0000-0000-0000-000000000001",
		TurnID:    &turnID,
		RunID:     "run-1",
		RequestID: "req-1",
		Kind:      "tool",
		Title:     &title,
		Detail:    &detail,
		Reason:    nil,
		Options: []RemoteRequestOption{
			{ID: "allow", Title: "允许", Role: "approve"},
			{ID: "allow_always", Title: "本会话始终允许", Role: "approveAlways"},
			{ID: "deny", Title: "拒绝", Role: "decline"},
		},
		Questions: []RemoteQuestion{},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal RemoteRequest: %v", err)
	}

	var reqDec RemoteRequest
	if err := json.Unmarshal(data, &reqDec); err != nil {
		t.Fatalf("unmarshal RemoteRequest: %v", err)
	}
	if reqDec.RequestID != "req-1" || len(reqDec.Options) != 3 || len(reqDec.Questions) != 0 {
		t.Errorf("unexpected RemoteRequest decoded: %+v", reqDec)
	}

	// 2. RequestsListResult 空切片输出为 [] 而非 null
	emptyListRes := RequestsListResult{
		ThreadID: "00000000-0000-0000-0000-000000000001",
		Requests: []RemoteRequest{},
	}
	listBytes, err := json.Marshal(emptyListRes)
	if err != nil {
		t.Fatalf("marshal RequestsListResult: %v", err)
	}
	if string(listBytes) != `{"threadID":"00000000-0000-0000-0000-000000000001","requests":[]}` {
		t.Errorf("expected empty array [] for requests, got %s", string(listBytes))
	}

	// 3. RequestsRespondParams 与 RequestsRespondResult 往返
	optID := "allow"
	respondParams := RequestsRespondParams{
		ThreadID:  "00000000-0000-0000-0000-000000000001",
		RunID:     "run-1",
		RequestID: "req-1",
		OptionID:  &optID,
	}
	paramBytes, err := json.Marshal(respondParams)
	if err != nil {
		t.Fatalf("marshal RequestsRespondParams: %v", err)
	}
	var decParam RequestsRespondParams
	if err := json.Unmarshal(paramBytes, &decParam); err != nil {
		t.Fatalf("unmarshal RequestsRespondParams: %v", err)
	}
	if decParam.RequestID != "req-1" || decParam.OptionID == nil || *decParam.OptionID != "allow" {
		t.Errorf("unexpected decoded RequestsRespondParams: %+v", decParam)
	}

	respondResult := RequestsRespondResult{
		ThreadID:  "00000000-0000-0000-0000-000000000001",
		RequestID: "req-1",
		Kind:      "tool",
		Status:    RequestStatusAccepted,
	}
	resBytes, err := json.Marshal(respondResult)
	if err != nil {
		t.Fatalf("marshal RequestsRespondResult: %v", err)
	}
	if string(resBytes) != `{"threadID":"00000000-0000-0000-0000-000000000001","requestID":"req-1","kind":"tool","status":"accepted"}` {
		t.Errorf("unexpected RequestsRespondResult JSON: %s", string(resBytes))
	}

	// 4. RequestsChangedParams 空切片输出为 []
	changedParams := RequestsChangedParams{
		ThreadID: "00000000-0000-0000-0000-000000000001",
		Requests: []RemoteRequest{},
	}
	changedBytes, err := json.Marshal(changedParams)
	if err != nil {
		t.Fatalf("marshal RequestsChangedParams: %v", err)
	}
	if string(changedBytes) != `{"threadID":"00000000-0000-0000-0000-000000000001","requests":[]}` {
		t.Errorf("expected empty array [] for changed requests, got %s", string(changedBytes))
	}
}

func TestActionStatusTypesRoundTrip(t *testing.T) {
	// 1. ActionStatusParams 序列化与反序列化
	paramsJSON := `{"actionID":"00000000-0000-0000-0000-000000000001"}`
	var params ActionStatusParams
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		t.Fatalf("unmarshal ActionStatusParams error: %v", err)
	}
	if params.ActionID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("unexpected ActionID: %s", params.ActionID)
	}
	paramBytes, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal ActionStatusParams error: %v", err)
	}
	if string(paramBytes) != paramsJSON {
		t.Errorf("expected %s, got %s", paramsJSON, string(paramBytes))
	}

	// 2. 最小 ActionStatusResult 仅包含 actionID 与 state，不包含任何多余的 null 字段
	minimalRes := ActionStatusResult{
		ActionID: "00000000-0000-0000-0000-000000000001",
		State:    ActionStateUnknown,
	}
	minBytes, err := json.Marshal(minimalRes)
	if err != nil {
		t.Fatalf("marshal minimal ActionStatusResult error: %v", err)
	}
	expectedMinJSON := `{"actionID":"00000000-0000-0000-0000-000000000001","state":"unknown"}`
	if string(minBytes) != expectedMinJSON {
		t.Errorf("expected %s, got %s", expectedMinJSON, string(minBytes))
	}

	// 3. 完整 ActionStatusResult 包含全部字段与 Error
	threadID := "11111111-1111-1111-1111-111111111111"
	messageID := "22222222-2222-2222-2222-222222222222"
	turnID := "33333333-3333-3333-3333-333333333333"
	followUpID := "44444444-4444-4444-4444-444444444444"
	taskID := "55555555-5555-5555-5555-555555555555"
	goalID := "66666666-6666-6666-6666-666666666666"
	revision := 3
	fullRes := ActionStatusResult{
		ActionID:   "00000000-0000-0000-0000-000000000001",
		State:      ActionStateFailed,
		ThreadID:   &threadID,
		MessageID:  &messageID,
		TurnID:     &turnID,
		FollowUpID: &followUpID,
		TaskID:     &taskID,
		GoalID:     &goalID,
		Revision:   &revision,
		Error: &ActionError{
			Code:    ErrCodeResourceNotFound,
			Message: "Thread not found",
		},
	}
	fullBytes, err := json.Marshal(fullRes)
	if err != nil {
		t.Fatalf("marshal full ActionStatusResult error: %v", err)
	}
	var decodedFull ActionStatusResult
	if err := json.Unmarshal(fullBytes, &decodedFull); err != nil {
		t.Fatalf("unmarshal full ActionStatusResult error: %v", err)
	}
	if decodedFull.ActionID != fullRes.ActionID || decodedFull.State != ActionStateFailed {
		t.Errorf("mismatch basic fields: %+v", decodedFull)
	}
	if decodedFull.ThreadID == nil || *decodedFull.ThreadID != threadID {
		t.Errorf("mismatch threadID")
	}
	if decodedFull.Revision == nil || *decodedFull.Revision != 3 {
		t.Errorf("mismatch revision")
	}
	if decodedFull.Error == nil || decodedFull.Error.Code != ErrCodeResourceNotFound || decodedFull.Error.Message != "Thread not found" {
		t.Errorf("mismatch error: %+v", decodedFull.Error)
	}
}

func TestInterruptTypesRoundTrip(t *testing.T) {
	// 1. InterruptParams 序列化与反序列化
	paramsJSON := `{"threadID":"11111111-1111-1111-1111-111111111111","turnID":"22222222-2222-2222-2222-222222222222"}`
	var params InterruptParams
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
		t.Fatalf("unmarshal InterruptParams error: %v", err)
	}
	if params.ThreadID != "11111111-1111-1111-1111-111111111111" || params.TurnID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("unexpected InterruptParams: %+v", params)
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal InterruptParams error: %v", err)
	}
	if string(paramsBytes) != paramsJSON {
		t.Errorf("expected %s, got %s", paramsJSON, string(paramsBytes))
	}

	// 2. InterruptResult (stopping) 序列化与反序列化
	resStoppingJSON := `{"threadID":"11111111-1111-1111-1111-111111111111","turnID":"22222222-2222-2222-2222-222222222222","state":"stopping"}`
	var resStopping InterruptResult
	if err := json.Unmarshal([]byte(resStoppingJSON), &resStopping); err != nil {
		t.Fatalf("unmarshal InterruptResult stopping error: %v", err)
	}
	if resStopping.State != InterruptStateStopping {
		t.Errorf("expected state %s, got %s", InterruptStateStopping, resStopping.State)
	}
	stoppingBytes, err := json.Marshal(resStopping)
	if err != nil {
		t.Fatalf("marshal InterruptResult stopping error: %v", err)
	}
	if string(stoppingBytes) != resStoppingJSON {
		t.Errorf("expected %s, got %s", resStoppingJSON, string(stoppingBytes))
	}

	// 3. InterruptResult (already_stopped) 序列化与反序列化
	resStoppedJSON := `{"threadID":"11111111-1111-1111-1111-111111111111","turnID":"22222222-2222-2222-2222-222222222222","state":"already_stopped"}`
	var resStopped InterruptResult
	if err := json.Unmarshal([]byte(resStoppedJSON), &resStopped); err != nil {
		t.Fatalf("unmarshal InterruptResult already_stopped error: %v", err)
	}
	if resStopped.State != InterruptStateAlreadyStopped {
		t.Errorf("expected state %s, got %s", InterruptStateAlreadyStopped, resStopped.State)
	}
	stoppedBytes, err := json.Marshal(resStopped)
	if err != nil {
		t.Fatalf("marshal InterruptResult already_stopped error: %v", err)
	}
	if string(stoppedBytes) != resStoppedJSON {
		t.Errorf("expected %s, got %s", resStoppedJSON, string(stoppedBytes))
	}
}

func TestThreadsQueueSnapshotRoundTrip(t *testing.T) {
	// 1. 包含 FollowUp 的 JSON 反序列化与序列化往返
	sampleJSON := `{
		"threadID": "00000000-0000-0000-0000-000000000001",
		"revision": 3,
		"followUps": [
			{
				"id": "11111111-1111-1111-1111-111111111111",
				"text": "测试跟进",
				"attachments": [
					{
						"id": "22222222-2222-2222-2222-222222222222",
						"name": "file.txt",
						"mimeType": "text/plain",
						"byteCount": 128
					}
				],
				"createdAt": "2026-10-17T03:00:00Z",
				"bundleID": null
			}
		]
	}`

	var snap ThreadsQueueSnapshot
	if err := json.Unmarshal([]byte(sampleJSON), &snap); err != nil {
		t.Fatalf("unmarshal ThreadsQueueSnapshot 失败: %v", err)
	}
	if snap.ThreadID != "00000000-0000-0000-0000-000000000001" || snap.Revision != 3 || len(snap.FollowUps) != 1 {
		t.Errorf("unexpected ThreadsQueueSnapshot: %+v", snap)
	}
	if snap.FollowUps[0].Text != "测试跟进" || len(snap.FollowUps[0].Attachments) != 1 {
		t.Errorf("unexpected follow-up item: %+v", snap.FollowUps[0])
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal ThreadsQueueSnapshot 失败: %v", err)
	}
	var roundTrip ThreadsQueueSnapshot
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal roundtrip ThreadsQueueSnapshot 失败: %v", err)
	}
	if roundTrip.ThreadID != snap.ThreadID || roundTrip.Revision != snap.Revision || len(roundTrip.FollowUps) != 1 {
		t.Errorf("roundtrip mismatch: %+v", roundTrip)
	}

	// 2. 空切片与 nil 切片序列化时必须输出 [] 而不是 null
	emptySnap := ThreadsQueueSnapshot{
		ThreadID:  "00000000-0000-0000-0000-000000000001",
		Revision:  1,
		FollowUps: []FollowUp{},
	}
	emptyBytes, err := json.Marshal(emptySnap)
	if err != nil {
		t.Fatalf("marshal empty ThreadsQueueSnapshot 失败: %v", err)
	}
	expectedSnapJSON := `{"threadID":"00000000-0000-0000-0000-000000000001","revision":1,"followUps":[]}`
	var emptyMap, expectedEmptyMap map[string]any
	if err := json.Unmarshal(emptyBytes, &emptyMap); err != nil {
		t.Fatalf("unmarshal empty map: %v", err)
	}
	if err := json.Unmarshal([]byte(expectedSnapJSON), &expectedEmptyMap); err != nil {
		t.Fatalf("unmarshal expected empty map: %v", err)
	}
	if !reflect.DeepEqual(emptyMap, expectedEmptyMap) {
		t.Errorf("expected %v, got %v", expectedEmptyMap, emptyMap)
	}

	nilSnap := ThreadsQueueSnapshot{
		ThreadID:  "00000000-0000-0000-0000-000000000001",
		Revision:  1,
		FollowUps: nil,
	}
	nilBytes, err := json.Marshal(nilSnap)
	if err != nil {
		t.Fatalf("marshal nil ThreadsQueueSnapshot 失败: %v", err)
	}
	var nilMap, expectedNilMap map[string]any
	if err := json.Unmarshal(nilBytes, &nilMap); err != nil {
		t.Fatalf("unmarshal nil map: %v", err)
	}
	if err := json.Unmarshal([]byte(expectedSnapJSON), &expectedNilMap); err != nil {
		t.Fatalf("unmarshal expected nil map: %v", err)
	}
	if !reflect.DeepEqual(nilMap, expectedNilMap) {
		t.Errorf("expected %v, got %v", expectedNilMap, nilMap)
	}
}

func TestThreadsCommandsResultRoundTrip(t *testing.T) {
	// 1. 完整结果（含 Error 为 nil 时输出 null）
	cmdJSON := `{
		"commands": [
			{
				"name": "help",
				"detail": "Show help info",
				"isBuiltIn": true,
				"kind": "builtin"
			}
		],
		"recent": ["help", "status"],
		"loading": false,
		"error": null
	}`

	var res ThreadsCommandsResult
	if err := json.Unmarshal([]byte(cmdJSON), &res); err != nil {
		t.Fatalf("unmarshal ThreadsCommandsResult 失败: %v", err)
	}
	if len(res.Commands) != 1 || len(res.Recent) != 2 || res.Loading || res.Error != nil {
		t.Errorf("unexpected ThreadsCommandsResult: %+v", res)
	}
	if res.Commands[0].Name != "help" || res.Commands[0].Kind != "builtin" || !res.Commands[0].IsBuiltIn {
		t.Errorf("unexpected command: %+v", res.Commands[0])
	}

	outBytes, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal ThreadsCommandsResult 失败: %v", err)
	}
	var roundTrip ThreadsCommandsResult
	if err := json.Unmarshal(outBytes, &roundTrip); err != nil {
		t.Fatalf("unmarshal roundtrip ThreadsCommandsResult 失败: %v", err)
	}
	if len(roundTrip.Commands) != 1 || len(roundTrip.Recent) != 2 || roundTrip.Error != nil {
		t.Errorf("roundtrip mismatch: %+v", roundTrip)
	}

	// 2. 带 Error 字符串和空切片
	errMsg := "failed to load skills"
	errRes := ThreadsCommandsResult{
		Commands: nil,
		Recent:   nil,
		Loading:  false,
		Error:    &errMsg,
	}
	errBytes, err := json.Marshal(errRes)
	if err != nil {
		t.Fatalf("marshal error ThreadsCommandsResult 失败: %v", err)
	}
	expectedJSON := `{"commands":[],"recent":[],"loading":false,"error":"failed to load skills"}`
	var gotMap, expectedMap map[string]any
	if err := json.Unmarshal(errBytes, &gotMap); err != nil {
		t.Fatalf("unmarshal got error: %v", err)
	}
	if err := json.Unmarshal([]byte(expectedJSON), &expectedMap); err != nil {
		t.Fatalf("unmarshal expected error: %v", err)
	}
	if !reflect.DeepEqual(gotMap, expectedMap) {
		t.Errorf("expected %v, got %v", expectedMap, gotMap)
	}
}

func TestThreadsQueueInterruptAndSendResultRoundTrip(t *testing.T) {
	turnID := "22222222-2222-2222-2222-222222222222"
	followUpID := "33333333-3333-3333-3333-333333333333"
	intRes := ThreadsQueueInterruptAndSendResult{
		ActionID:   "00000000-0000-0000-0000-000000000001",
		ThreadID:   "11111111-1111-1111-1111-111111111111",
		State:      ThreadsQueueInterruptStateStopping,
		TurnID:     &turnID,
		FollowUpID: &followUpID,
		Queue: ThreadsQueueSnapshot{
			ThreadID:  "11111111-1111-1111-1111-111111111111",
			Revision:  2,
			FollowUps: []FollowUp{},
		},
	}

	data, err := json.Marshal(intRes)
	if err != nil {
		t.Fatalf("marshal ThreadsQueueInterruptAndSendResult 失败: %v", err)
	}

	var dec ThreadsQueueInterruptAndSendResult
	if err := json.Unmarshal(data, &dec); err != nil {
		t.Fatalf("unmarshal ThreadsQueueInterruptAndSendResult 失败: %v", err)
	}

	if dec.ActionID != intRes.ActionID || dec.ThreadID != intRes.ThreadID || dec.State != ThreadsQueueInterruptStateStopping {
		t.Errorf("unexpected basic fields: %+v", dec)
	}
	if dec.TurnID == nil || *dec.TurnID != turnID || dec.FollowUpID == nil || *dec.FollowUpID != followUpID {
		t.Errorf("unexpected turnID or followUpID: %+v", dec)
	}
	if dec.Queue.ThreadID != intRes.ThreadID || dec.Queue.Revision != 2 || len(dec.Queue.FollowUps) != 0 {
		t.Errorf("unexpected queue snapshot: %+v", dec.Queue)
	}

	// 验证其它 state 枚举常量
	if ThreadsQueueInterruptStateQueued != "queued" || ThreadsQueueInterruptStateSentToSwarm != "sent_to_swarm" {
		t.Errorf("unexpected interrupt state constants")
	}
}

// TestThreadMessageErrKindRoundTrip 验证错误消息的失败类别按 errKind 序列化：
// 有类别时出现在 JSON 里并能原样解析回来，为空时省略字段（老客户端不受影响）。
func TestThreadMessageErrKindRoundTrip(t *testing.T) {
	kind := "error"
	errKind := "rate_limited"
	msg := ThreadMessage{
		ID:        "m1",
		Role:      "assistant",
		Text:      "出错：429 too many requests",
		CreatedAt: "2026-10-20T01:00:00Z",
		TurnIndex: 1,
		Kind:      &kind,
		ErrKind:   &errKind,
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal into map error: %v", err)
	}
	if fields["errKind"] != "rate_limited" {
		t.Fatalf("errKind = %v, want rate_limited", fields["errKind"])
	}

	var got ThreadMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.ErrKind == nil || *got.ErrKind != "rate_limited" {
		t.Fatalf("round trip ErrKind = %v", got.ErrKind)
	}

	// 未分类的消息不带这个字段，界面对这类消息沿用关键词识别。
	msg.ErrKind = nil
	raw, err = json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal without errKind error: %v", err)
	}
	fields = map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal into map error: %v", err)
	}
	if _, ok := fields["errKind"]; ok {
		t.Fatalf("errKind must be omitted when nil, got %v", fields["errKind"])
	}
}
