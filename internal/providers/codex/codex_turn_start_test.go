package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/testenv"
)

func TestBuildTurnInput(t *testing.T) {
	t.Run("无图片时只有文本项", func(t *testing.T) {
		input := buildTurnInput(harness.Turn{Prompt: "你好"})
		if len(input) != 1 {
			t.Fatalf("expected 1 input item, got %d", len(input))
		}
		if input[0]["type"] != "text" || input[0]["text"] != "你好" {
			t.Fatalf("unexpected text item: %+v", input[0])
		}
		if _, ok := input[0]["path"]; ok {
			t.Fatalf("text item must not carry path: %+v", input[0])
		}
	})

	t.Run("图片项追加在文本项之后", func(t *testing.T) {
		wantPaths := []string{"/tmp/a.png", "/tmp/b.jpg"}
		input := buildTurnInput(harness.Turn{Prompt: "看图", Images: wantPaths})
		if len(input) != 1+len(wantPaths) {
			t.Fatalf("expected %d input items, got %d", 1+len(wantPaths), len(input))
		}
		if input[0]["type"] != "text" || input[0]["text"] != "看图" {
			t.Fatalf("unexpected text item: %+v", input[0])
		}
		for i, want := range wantPaths {
			item := input[i+1]
			// schema: UserInput -> LocalImageUserInput，type=localImage 且 path 必填
			if item["type"] != "localImage" {
				t.Fatalf("item %d: expected type localImage, got %v", i+1, item["type"])
			}
			if item["path"] != want {
				t.Fatalf("item %d: expected path %q, got %v", i+1, want, item["path"])
			}
			if _, ok := item["text"]; ok {
				t.Fatalf("image item must not carry text: %+v", item)
			}
		}
	})

	t.Run("空白图片路径被忽略", func(t *testing.T) {
		input := buildTurnInput(harness.Turn{Prompt: "x", Images: []string{"  ", "", "/tmp/c.webp\n"}})
		if len(input) != 2 {
			t.Fatalf("expected text item plus one image item, got %d items", len(input))
		}
		// 路径两端的空白会先去掉再下发
		if input[1]["path"] != "/tmp/c.webp" {
			t.Fatalf("unexpected second item: %+v", input[1])
		}
	})

	t.Run("非图片附件不作为本地图片下发", func(t *testing.T) {
		input := buildTurnInput(harness.Turn{Prompt: "看附件", Images: []string{"/tmp/a.png", "/tmp/report.pdf"}})
		// 只剩文本项 + a.png：PDF 的路径已写进 prompt，不能当本地图片下发。
		if len(input) != 2 {
			t.Fatalf("expected text item plus one image item, got %d items: %+v", len(input), input)
		}
		if input[1]["type"] != "localImage" || input[1]["path"] != "/tmp/a.png" {
			t.Fatalf("unexpected second item: %+v", input[1])
		}
	})

	t.Run("无扩展名的png仍作为本地图片下发", func(t *testing.T) {
		// 会话媒体目录里的历史文件名是不带扩展名的 UUID，只能靠魔数识别。
		path := filepath.Join(t.TempDir(), "f47ac10b-58cc-4372-a567-0e02b2c3d479")
		if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
			t.Fatalf("写测试图片失败: %v", err)
		}
		input := buildTurnInput(harness.Turn{Prompt: "看图", Images: []string{path}})
		if len(input) != 2 {
			t.Fatalf("expected text item plus one image item, got %d items: %+v", len(input), input)
		}
		if input[1]["type"] != "localImage" || input[1]["path"] != path {
			t.Fatalf("unexpected second item: %+v", input[1])
		}
	})
}

func TestBuildTurnStartParamsBaseline(t *testing.T) {
	turn := harness.Turn{
		Prompt:    "hello",
		Model:     "gpt-5-codex",
		Effort:    "high",
		FastMode:  true,
		SpeedTier: "priority",
	}
	params := buildTurnStartParams(turn, "th_1")

	if params["threadId"] != "th_1" {
		t.Fatalf("unexpected threadId: %v", params["threadId"])
	}
	if params["model"] != "gpt-5-codex" {
		t.Fatalf("unexpected model: %v", params["model"])
	}
	if params["effort"] != "high" {
		t.Fatalf("unexpected effort: %v", params["effort"])
	}
	if params["serviceTierForTurn"] != "priority" {
		t.Fatalf("unexpected serviceTierForTurn: %v", params["serviceTierForTurn"])
	}
	if _, ok := params["collaborationMode"]; ok {
		t.Fatalf("build 模式不应下发 collaborationMode: %+v", params)
	}
	input, ok := params["input"].([]map[string]any)
	if !ok || len(input) != 1 {
		t.Fatalf("unexpected input: %#v", params["input"])
	}
}

func TestBuildTurnStartParamsOmitsEmptyFields(t *testing.T) {
	params := buildTurnStartParams(harness.Turn{Prompt: "hi"}, "")
	if _, ok := params["model"]; ok {
		t.Fatalf("空 model 不应下发: %+v", params)
	}
	if _, ok := params["effort"]; ok {
		t.Fatalf("空 effort 不应下发: %+v", params)
	}
	if _, ok := params["serviceTierForTurn"]; ok {
		t.Fatalf("非快速模式不应下发 serviceTierForTurn: %+v", params)
	}
}

func TestBuildCollaborationMode(t *testing.T) {
	tests := []struct {
		name     string
		turn     harness.Turn
		wantNil  bool
		wantMode string
		wantEff  string
	}{
		{
			name:    "build 模式不下发",
			turn:    harness.Turn{InteractionMode: "build", Model: "gpt-5-codex"},
			wantNil: true,
		},
		{
			name:    "空模式等同 build",
			turn:    harness.Turn{Model: "gpt-5-codex"},
			wantNil: true,
		},
		{
			// schema 要求 CollaborationMode.settings.model 必填，缺 model 时退化为普通回合
			name:    "plan 模式缺 model 时退化",
			turn:    harness.Turn{InteractionMode: "plan"},
			wantNil: true,
		},
		{
			name:     "plan 模式带 model 与 effort",
			turn:     harness.Turn{InteractionMode: "plan", Model: "gpt-5-codex", Effort: "medium"},
			wantMode: "plan",
			wantEff:  "medium",
		},
		{
			name:     "plan 模式只有 model",
			turn:     harness.Turn{InteractionMode: "plan", Model: "gpt-5-codex"},
			wantMode: "plan",
		},
		{
			name:     "plan 前后空白也识别",
			turn:     harness.Turn{InteractionMode: " plan ", Model: "gpt-5-codex"},
			wantMode: "plan",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode := buildCollaborationMode(tc.turn)
			if tc.wantNil {
				if mode != nil {
					t.Fatalf("expected nil collaborationMode, got %+v", mode)
				}
				return
			}
			if mode == nil {
				t.Fatal("expected collaborationMode, got nil")
			}
			if mode["mode"] != tc.wantMode {
				t.Fatalf("expected mode %q, got %v", tc.wantMode, mode["mode"])
			}
			settings, ok := mode["settings"].(map[string]any)
			if !ok {
				t.Fatalf("settings is not a map: %#v", mode["settings"])
			}
			if settings["model"] != tc.turn.Model {
				t.Fatalf("expected settings.model %q, got %v", tc.turn.Model, settings["model"])
			}
			// developer_instructions 留空表示使用该模式内置指令
			if _, ok := settings["developer_instructions"]; ok {
				t.Fatalf("不应下发 developer_instructions: %+v", settings)
			}
			if tc.wantEff == "" {
				if _, ok := settings["reasoning_effort"]; ok {
					t.Fatalf("空 effort 不应下发 reasoning_effort: %+v", settings)
				}
				return
			}
			if settings["reasoning_effort"] != tc.wantEff {
				t.Fatalf("expected reasoning_effort %q, got %v", tc.wantEff, settings["reasoning_effort"])
			}
		})
	}
}

func TestBuildTurnStartParamsPlanMode(t *testing.T) {
	turn := harness.Turn{
		Prompt:          "规划一下",
		InteractionMode: "plan",
		Model:           "gpt-5-codex",
		Effort:          "high",
	}
	params := buildTurnStartParams(turn, "th_1")

	mode, ok := params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("plan 回合应下发 collaborationMode: %+v", params)
	}
	if mode["mode"] != "plan" {
		t.Fatalf("expected mode plan, got %v", mode["mode"])
	}
	settings, _ := mode["settings"].(map[string]any)
	if settings["model"] != "gpt-5-codex" || settings["reasoning_effort"] != "high" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
}

// turnStartLogRequest 是日志里 turn/start 请求的可断言视图。
type turnStartLogRequest struct {
	Method string `json:"method"`
	Params struct {
		ThreadID string `json:"threadId"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
		Input    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Path string `json:"path"`
		} `json:"input"`
		CollaborationMode *struct {
			Mode     string `json:"mode"`
			Settings struct {
				Model           string `json:"model"`
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"collaborationMode"`
	} `json:"params"`
}

// firstTurnStart 返回日志里第一条 turn/start 请求。
func firstTurnStart(t *testing.T, logPath string) turnStartLogRequest {
	t.Helper()
	for _, line := range readLines(t, logPath) {
		var req turnStartLogRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.Method == "turn/start" {
			return req
		}
	}
	t.Fatalf("no turn/start request found in %s", logPath)
	return turnStartLogRequest{}
}

func TestRunSendsLocalImages(t *testing.T) {
	testenv.TempHome(t)
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")

	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return codexAppServerCommand(ctx, appServerTurnLog, logPath)
	}}

	h := New(Options{})
	defer h.Close()

	var texts []string
	turn := harness.Turn{
		WorkDir: t.TempDir(),
		Mode:    harness.ModeAgent,
		Prompt:  "看这两张图",
		Images:  []string{"/tmp/one.png", "/tmp/two.jpg"},
		Sandbox: sb,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Run(ctx, turn, func(e harness.Event) {
		if e.Type == harness.EventText {
			texts = append(texts, e.Text)
		}
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	req := firstTurnStart(t, logPath)
	if len(req.Params.Input) != 3 {
		t.Fatalf("expected 3 input items (text + 2 images), got %d: %+v", len(req.Params.Input), req.Params.Input)
	}
	if req.Params.Input[0].Type != "text" || req.Params.Input[0].Text != "看这两张图" {
		t.Fatalf("unexpected first input item: %+v", req.Params.Input[0])
	}
	for i, want := range []string{"/tmp/one.png", "/tmp/two.jpg"} {
		item := req.Params.Input[i+1]
		if item.Type != "localImage" || item.Path != want {
			t.Fatalf("input item %d: expected localImage %q, got %+v", i+1, want, item)
		}
	}
	if strings.Join(texts, "") != "OK" {
		t.Fatalf("expected streamed text OK, got %v", texts)
	}
}

func TestRunSendsPlanCollaborationMode(t *testing.T) {
	testenv.TempHome(t)
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")

	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return codexAppServerCommand(ctx, appServerTurnLog, logPath)
	}}

	h := New(Options{})
	defer h.Close()

	turn := harness.Turn{
		WorkDir:         t.TempDir(),
		Mode:            harness.ModeAgent,
		Prompt:          "给出实施计划",
		InteractionMode: "plan",
		Model:           "gpt-5-codex",
		Effort:          "high",
		Sandbox:         sb,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Run(ctx, turn, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	req := firstTurnStart(t, logPath)
	if req.Params.CollaborationMode == nil {
		t.Fatal("plan 回合的 turn/start 缺少 collaborationMode")
	}
	if req.Params.CollaborationMode.Mode != "plan" {
		t.Fatalf("expected mode plan, got %q", req.Params.CollaborationMode.Mode)
	}
	if req.Params.CollaborationMode.Settings.Model != "gpt-5-codex" {
		t.Fatalf("unexpected settings.model: %+v", req.Params.CollaborationMode.Settings)
	}
	if req.Params.CollaborationMode.Settings.ReasoningEffort != "high" {
		t.Fatalf("unexpected settings.reasoning_effort: %+v", req.Params.CollaborationMode.Settings)
	}
}
