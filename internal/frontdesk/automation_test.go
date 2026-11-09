package frontdesk

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type fakeAutomationService struct{ input AutomationInput }

func (f *fakeAutomationService) CreateAutomation(_ context.Context, in AutomationInput) (AutomationResult, error) {
	f.input = in
	return AutomationResult{ID: "a", Name: in.Name}, nil
}

func TestCreateAutomationTool(t *testing.T) {
	if _, ok := NewToolbox(&Desk{}).Get("create_automation"); ok {
		t.Fatal("没有服务时不应注册工具")
	}
	service := &fakeAutomationService{}
	tool, ok := NewToolbox(&Desk{Automations: service}).Get("create_automation")
	if !ok || !tool.Write {
		t.Fatal("缺少写工具")
	}
	result, err := tool.Run(context.Background(), json.RawMessage(`{"project_id":"p","name":"n","prompt":"work","schedule":"daily:09:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := AutomationInput{ProjectID: "p", Name: "n", Prompt: "work", Schedule: "daily:09:00"}
	if !reflect.DeepEqual(service.input, want) || result != (AutomationResult{ID: "a", Name: "n"}) {
		t.Fatalf("参数或结果不符: %+v %+v", service.input, result)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"project_id":"p","name":"n","prompt":"work"}`)); err == nil {
		t.Fatal("缺少 schedule 应报错")
	}
}
