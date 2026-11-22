package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestModelPickerPersonaButton(t *testing.T) {
	t.Run("输入框有人设时模型浮窗顶栏显示人设按钮并写入", func(t *testing.T) {
		testenv.TempHome(t)
		t.Setenv("MCODE_DATA", t.TempDir())
		t.Cleanup(lastSelectionWriteWG.Wait)
		resetComposerModelPickerForTest(t)

		fake := NewFakeClient()
		fake.SetMagpiePersonas(personaMenuTestPersonas())
		s, root := buildRoot(fake)
		t.Cleanup(s.close)
		tt := ui.NewTester(root, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
		s.selectThread("t1")

		waitFor(t, tt, func() bool {
			_, ok := tt.Find("选择模型")
			return ok
		})
		if err := tt.Click("选择模型"); err != nil {
			t.Fatalf("打开模型浮窗失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, ok := tt.Find("人设")
			return ok
		})

		if err := tt.Click("人设"); err != nil {
			t.Fatalf("点击人设按钮失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, okNone := tt.Find("无人设")
			_, okStrict := tt.Find("严谨审阅")
			_, okCheerful := tt.Find("轻松搭档")
			return okNone && okStrict && okCheerful
		})

		if err := tt.Click("严谨审阅"); err != nil {
			t.Fatalf("选择人设失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			persona, saving := localThreadPersona(s, "t1")
			return persona == "strict" && !saving
		})
		if tid, prefs, ok := fake.LastComposerPrefsCall(); !ok || tid != "t1" || prefs.Persona != "strict" {
			t.Fatalf("人设写入错误: tid=%q persona=%q ok=%v", tid, prefs.Persona, ok)
		}
		waitFor(t, tt, func() bool {
			_, ok := tt.Find("严谨审阅")
			return !ok
		})
	})

	t.Run("回调触发OnPersona", func(t *testing.T) {
		var picked []string
		curPersona := ""
		state := &ModelPickerState{MenuOpen: true}
		tt := ui.NewTester(func(c *ui.Context) {
			renderModelPicker(c, ModelPickerProps{
				Mode:         ModelPickerSwarm,
				KeyPrefix:    "test-persona-",
				ScopeKey:     "test-persona",
				State:        state,
				Providers:    modelPickerTestProviders(),
				Selection:    ModelSelection{Provider: "claude", Model: "sonnet"},
				CapsuleLabel: "Sonnet",
				Personas: []MagpiePersona{
					{ID: "", Name: "空ID应跳过"},
					{ID: personaNoneValue, Name: "保留字应跳过"},
					{ID: "reviewer", Name: ""},
				},
				Persona: curPersona,
				OnPersona: func(id string) {
					picked = append(picked, id)
					curPersona = id
				},
			})
		}, 900, 700)

		waitFor(t, tt, func() bool {
			_, ok := tt.Find("人设")
			return ok
		})
		if err := tt.Click("人设"); err != nil {
			t.Fatalf("点击人设按钮失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, okNone := tt.Find("无人设")
			_, okID := tt.Find("reviewer")
			return okNone && okID
		})
		if _, ok := tt.Find("空ID应跳过"); ok {
			t.Fatal("不应渲染空 id 人设")
		}
		if _, ok := tt.Find("保留字应跳过"); ok {
			t.Fatal("不应渲染 id=none 的人设")
		}

		if err := tt.Click("reviewer"); err != nil {
			t.Fatalf("点击 reviewer 失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			return len(picked) == 1 && picked[0] == "reviewer" && !state.PersonaOpen
		})
	})

	t.Run("无人设或未传OnPersona时不出现人设按钮", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			personas  []MagpiePersona
			onPersona func(string)
		}{
			{name: "无人设", personas: nil, onPersona: func(string) {}},
			{name: "未传OnPersona", personas: personaMenuTestPersonas(), onPersona: nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				state := &ModelPickerState{MenuOpen: true}
				tt := ui.NewTester(func(c *ui.Context) {
					renderModelPicker(c, ModelPickerProps{
						Mode:          ModelPickerSwarm,
						KeyPrefix:     "test-hide-persona-",
						ScopeKey:      "test-hide-persona",
						State:         state,
						Providers:     modelPickerTestProviders(),
						Selection:     ModelSelection{Provider: "claude", Model: "sonnet"},
						CapsuleLabel:  "Sonnet",
						OnToggleSwarm: func(bool) {},
						Personas:      tc.personas,
						OnPersona:     tc.onPersona,
					})
				}, 900, 700)
				waitFor(t, tt, func() bool { return tt.HasText("编排") })
				if _, ok := tt.Find("人设"); ok {
					t.Fatalf("%s 时不应渲染人设按钮, texts=%v", tc.name, tt.Texts())
				}
			})
		}
	})
}
