package app

import (
	"context"
	"reflect"
	"testing"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestReviewSnapshotConversion(t *testing.T) {
	input := wire.ThreadsReviewGetResult{Scope: wire.ReviewScopeTurn, TurnID: "turn", BaseRevision: "base", HeadRevision: "head", Running: true, Unavailable: "reason", Files: []wire.ReviewFileMeta{{Path: "new.go", OrigPath: "old.go", Status: "R", Language: "go", OldSize: 10, NewSize: 20, OldHash: "old", NewHash: "new", Binary: true, TooLarge: true, Missing: true}}, File: &wire.ReviewFileContent{Path: "new.go", OldText: "old text", NewText: "new text", OldHash: "old", NewHash: "new", Binary: true, TooLarge: true, Missing: true}}
	want := mcodeui.ReviewSnapshot{Scope: "turn", TurnID: "turn", BaseRevision: "base", HeadRevision: "head", Running: true, Unavailable: "reason", Files: []mcodeui.ReviewFile{{Path: "new.go", OrigPath: "old.go", Status: "R", Language: "go", OldSize: 10, NewSize: 20, OldHash: "old", NewHash: "new", Binary: true, TooLarge: true, Missing: true}}, File: &mcodeui.ReviewFileContent{Path: "new.go", OldText: "old text", NewText: "new text", OldHash: "old", NewHash: "new", Binary: true, TooLarge: true, Missing: true}}
	got := reviewSnapshotFromWire(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("转换丢字段: %+v", got)
	}
	got.Files[0].Path = "changed"
	got.File.Path = "changed"
	if input.Files[0].Path != "new.go" || input.File.Path != "new.go" {
		t.Fatal("转换结果共享可变数据")
	}
	empty := reviewSnapshotFromWire(wire.ThreadsReviewGetResult{})
	if empty.Files == nil || empty.File != nil {
		t.Fatal("空列表或按需全文不符")
	}
	if _, err := (&Client{}).ReviewGet(context.Background(), "thread", "turn", "", ""); err == nil {
		t.Fatal("未配置 backend 未报错")
	}
}
