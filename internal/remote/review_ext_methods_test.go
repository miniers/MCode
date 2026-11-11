package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

type reviewExtTestBackend struct {
	Backend
	result wire.ThreadsReviewGetResult
	params wire.ThreadsReviewGetParams
	calls  int
	err    error
}

func (b *reviewExtTestBackend) ReviewGet(ctx context.Context, p wire.ThreadsReviewGetParams) (wire.ThreadsReviewGetResult, error) {
	b.params = p
	b.calls++
	return b.result, b.err
}

func TestReviewSchemaAndInvalidParams(t *testing.T) {
	backend := &reviewExtTestBackend{result: wire.ThreadsReviewGetResult{Scope: wire.ReviewScopeTurn, Files: []wire.ReviewFileMeta{}}}
	d, conn := newGitExtTestDispatcher(backend)
	if !d.methods["threads.review.get"] {
		t.Fatal("未注册审阅方法")
	}
	advertised := false
	for _, method := range ImplementedMethods() {
		advertised = advertised || method == "threads.review.get"
	}
	if !advertised {
		t.Fatal("capabilities 未声明审阅方法")
	}
	valid := `{"threadID":"` + revertExtTestThreadID + `","scope":"turn"}`
	for _, params := range []string{
		`{"scope":"turn"}`, `{"threadID":"invalid","scope":"turn"}`, `{"threadID":"` + revertExtTestThreadID + `"}`,
		strings.Replace(valid, `"turn"`, `"invalid"`, 1), strings.TrimSuffix(valid, "}") + `,"unknown":1}`,
		strings.TrimSuffix(valid, "}") + `,"turnID":"invalid"}`, strings.TrimSuffix(valid, "}") + `,"turnID":null}`, strings.TrimSuffix(valid, "}") + `,"path":null}`,
		`{"threadID":"` + revertExtTestThreadID + `","scope":"session","turnID":"` + revertExtTestTurnID + `"}`,
	} {
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.review.get","params":`+params+`,"id":1}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s: %+v", params, resp.Error)
		}
	}
	if backend.calls != 0 {
		t.Fatal("非法参数调用了后端")
	}
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.review.get","params":`+valid+`,"id":1}`)
	var result wire.ThreadsReviewGetResult
	gitExtResult(t, resp, &result)
	if backend.calls != 1 || backend.params.ThreadID != revertExtTestThreadID || result.Scope != wire.ReviewScopeTurn {
		t.Fatalf("结果或参数不符: %+v", result)
	}
	backend.err = errors.New("业务拒绝")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.review.get","params":`+valid+`,"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected || resp.Error.Message != "业务拒绝" {
		t.Fatalf("业务拒绝: %+v", resp.Error)
	}
	backend.err = ExtInvalidParams("路径越界")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.review.get","params":`+valid+`,"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("路径错误: %+v", resp.Error)
	}
	d2, c2 := newGitExtTestDispatcher(&fakeBackend{})
	resp = callExtension(t, d2, c2, `{"jsonrpc":"2.0","method":"threads.review.get","params":`+valid+`,"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("无能力后端: %+v", resp.Error)
	}

	data, e := os.ReadFile("../../docs/remote-control.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var schema struct {
		OneOf []map[string]string        `json:"oneOf"`
		Defs  map[string]json.RawMessage `json:"$defs"`
	}
	if e = json.Unmarshal(data, &schema); e != nil {
		t.Fatal(e)
	}
	// 请求与响应均须进入根协议和批量协议的联合类型。
	for _, name := range []string{"RemoteThreadsReviewGetRequest", "RemoteThreadsReviewGetResponse"} {
		if len(schema.Defs[name]) == 0 {
			t.Fatalf("缺少 %s", name)
		}
		if strings.Count(string(data), `"$ref": "#/$defs/`+name+`"`) != 2 {
			t.Fatalf("%s 未同步联合类型", name)
		}
	}
	var request struct {
		Properties map[string]struct {
			Const string `json:"const"`
		} `json:"properties"`
	}
	if e = json.Unmarshal(schema.Defs["RemoteThreadsReviewGetRequest"], &request); e != nil {
		t.Fatal(e)
	}
	if request.Properties["method"].Const != "threads.review.get" {
		t.Fatal("方法名不符")
	}
	for name, value := range map[string]any{
		"RemoteThreadsReviewGetParams": wire.ThreadsReviewGetParams{}, "RemoteThreadsReviewGetResult": wire.ThreadsReviewGetResult{}, "RemoteReviewFileMeta": wire.ReviewFileMeta{}, "RemoteReviewFileContent": wire.ReviewFileContent{},
	} {
		var def struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
			Additional bool                       `json:"additionalProperties"`
		}
		if e = json.Unmarshal(schema.Defs[name], &def); e != nil {
			t.Fatal(e)
		}
		typ := reflect.TypeOf(value)
		if len(def.Properties) != typ.NumField() || def.Additional {
			t.Fatalf("%s 字段数量或严格性不符", name)
		}
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			field := strings.Split(tag, ",")[0]
			if _, ok := def.Properties[field]; !ok {
				t.Fatalf("%s 缺少 %s", name, field)
			}
			required := false
			for _, r := range def.Required {
				required = required || r == field
			}
			if required == strings.Contains(tag, "omitempty") {
				t.Fatalf("%s.%s 必填约束不符", name, field)
			}
		}
	}
	var scope struct {
		Enum []string `json:"enum"`
	}
	if e = json.Unmarshal(schema.Defs["RemoteReviewScope"], &scope); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(scope.Enum, []string{"turn", "session", "uncommitted"}) {
		t.Fatal("scope schema 不符")
	}
}
