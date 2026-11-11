package kanban

import "testing"

func TestParseExecutorResultLastBlock(t *testing.T) {
	r, err := ParseExecutorResult("```json\n{\"status\":\"failed\"}\n```\n```json\n{\"status\":\"waiting\",\"wait_kind\":\"human\",\"wait_minutes\":999}\n```")
	if err != nil || r.Status != ResultWaiting || r.WaitMinutes != 120 {
		t.Fatalf("结果 %v，错误 %v", r, err)
	}
}

func TestExecutorResultValidate(t *testing.T) {
	cases := []struct {
		result ExecutorResult
		valid  bool
	}{
		{ExecutorResult{Status: ResultDone}, true},
		{ExecutorResult{Status: ResultDone, NoChange: true}, true},
		{ExecutorResult{Status: ResultFailed, NoChange: true}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "human", NoChange: true}, false},
		{ExecutorResult{Status: ResultDelegated, Followups: []Followup{{Title: "子任务"}}, NoChange: true}, false},
		{ExecutorResult{Status: ResultNeedsDecision, Question: "问题", Options: []DecisionOption{{Key: "A"}, {Key: "B"}}, NoChange: true}, false},
		{ExecutorResult{Status: ResultFailed}, true},
		{ExecutorResult{Status: "unknown"}, false},
		{ExecutorResult{Status: ResultNeedsDecision}, false},
		{ExecutorResult{Status: ResultNeedsDecision, Question: "问题", Options: []DecisionOption{{Key: "A"}}}, false},
		{ExecutorResult{Status: ResultNeedsDecision, Question: "问题", Options: []DecisionOption{{Key: "A"}, {Key: "B"}}}, true},
		{ExecutorResult{Status: ResultDelegated}, false},
		{ExecutorResult{Status: ResultDelegated, Followups: []Followup{{Title: "子任务"}}}, true},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "other"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine", WaitCheck: "Aspen 和 Birch 两个 bee 都已通过编排通知回报调查结果"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine", WaitCheck: "Aspen 已回报 [编排]"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine", WaitCheck: "rm marker"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine", WaitCheck: "ls marker"}, true},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine", WaitCheck: "true"}, true},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "human"}, true},
	}
	for _, tc := range cases {
		if (tc.result.Validate() == nil) != tc.valid {
			t.Errorf("结果 %#v 的校验不符合预期", tc.result)
		}
	}
}

func TestParseExecutorResultNoChange(t *testing.T) {
	r, err := ParseExecutorResult(`{"status":"done","no_change":true}`)
	if err != nil || !r.NoChange {
		t.Fatalf("结果 %v，错误 %v", r, err)
	}
	if _, err := ParseExecutorResult(`{"status":"failed","no_change":true}`); err == nil {
		t.Fatal("非 done 状态不允许 no_change")
	}
}
