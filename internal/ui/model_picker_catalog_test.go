package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/config"
)

func TestSettingsModelPickerCatalogPins(t *testing.T) {
	chat := &chatState{providers: []ProviderInfo{{ID: "mcode"}}}
	st := &SettingsState{chat: chat}
	st.cfg.Models.Pins = []ModelPin{{Provider: "mcode", ModelID: "model"}}
	setChatSettings(chat, st)
	t.Cleanup(func() { setChatSettings(chat, nil) })

	cat := settingsModelPickerCatalog(st)
	if !reflect.DeepEqual(cat.Pins, st.cfg.Models.Pins) {
		t.Fatalf("Pins = %v，期望 %v", cat.Pins, st.cfg.Models.Pins)
	}
	if len(cat.apply(ModelPickerProps{}).Pins) == 0 {
		t.Fatal("apply 后缺少精选模型")
	}
}

func TestSettingsModelPickerCatalogFallback(t *testing.T) {
	providers := settingsModelPickerCatalog(nil).Providers
	want := []ProviderInfo{
		{ID: config.ProviderClaude, Name: "Claude"},
		{ID: config.ProviderCodex, Name: "Codex"},
		{ID: config.ProviderAgy, Name: "agy"},
	}
	if !reflect.DeepEqual(providers, want) {
		t.Fatalf("Providers = %v，期望 %v", providers, want)
	}
}

func TestModelPickerUsesCatalog(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "renderModelPicker" {
				return true
			}
			if applied, ok := call.Args[1].(*ast.CallExpr); ok {
				if sel, ok := applied.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "apply" && fset.Position(sel.Sel.Pos()).Line == fset.Position(call.Pos()).Line {
					return true
				}
			}
			if props, ok := call.Args[1].(*ast.CompositeLit); ok {
				for _, elt := range props.Elts {
					field, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, keyOK := field.Key.(*ast.Ident)
					value, valueOK := field.Value.(*ast.Ident)
					if keyOK && valueOK && key.Name == "Inline" && value.Name == "true" {
						return true
					}
				}
			}
			t.Errorf("%s: 模型选择器必须使用公共目录或启用 Inline", fset.Position(call.Pos()))
			return true
		})
	}
}
