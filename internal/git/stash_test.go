package git

import (
	"context"
	"testing"
)

// TestStashListOrderAndMessage 覆盖：连续两次贮藏后，列表顺序是最新在前，
// 序号、说明、来源分支与创建时间都能解析出来。
func TestStashListOrderAndMessage(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "seed.txt", "第一次改动\n")
	if err := runner.StashPush(ctx, dir, "第一条贮藏"); err != nil {
		t.Fatalf("第一次贮藏失败: %v", err)
	}
	writeTestFile(t, dir, "seed.txt", "第二次改动\n")
	if err := runner.StashPush(ctx, dir, "第二条贮藏"); err != nil {
		t.Fatalf("第二次贮藏失败: %v", err)
	}

	entries, err := runner.StashList(ctx, dir)
	if err != nil {
		t.Fatalf("StashList 失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("贮藏数量 = %d, 期望 2: %+v", len(entries), entries)
	}
	if entries[0].Index != 0 || entries[0].Ref != "stash@{0}" {
		t.Errorf("第一条 = index %d ref %q, 期望 0 与 stash@{0}", entries[0].Index, entries[0].Ref)
	}
	if entries[0].Message != "第二条贮藏" {
		t.Errorf("第一条说明 = %q, 期望 第二条贮藏", entries[0].Message)
	}
	if entries[1].Index != 1 || entries[1].Ref != "stash@{1}" {
		t.Errorf("第二条 = index %d ref %q, 期望 1 与 stash@{1}", entries[1].Index, entries[1].Ref)
	}
	if entries[1].Message != "第一条贮藏" {
		t.Errorf("第二条说明 = %q, 期望 第一条贮藏", entries[1].Message)
	}
	for i, entry := range entries {
		if entry.Branch == "" {
			t.Errorf("第 %d 条没有解析出来源分支", i)
		}
		if entry.CreatedAt.IsZero() {
			t.Errorf("第 %d 条没有解析出创建时间", i)
		}
	}
}

// TestStashListEmpty 覆盖：没有贮藏记录时返回空列表且不报错。
func TestStashListEmpty(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	entries, err := runner.StashList(context.Background(), dir)
	if err != nil {
		t.Fatalf("StashList 失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("贮藏数量 = %d, 期望 0", len(entries))
	}
}

// TestStashApplyDropPopAt 覆盖：应用指定序号的贮藏、删除指定序号、应用并删除。
func TestStashApplyDropPopAt(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	original := readTestFile(t, dir, "seed.txt")
	writeTestFile(t, dir, "seed.txt", "第一次改动\n")
	if err := runner.StashPush(ctx, dir, "第一条贮藏"); err != nil {
		t.Fatalf("第一次贮藏失败: %v", err)
	}
	writeTestFile(t, dir, "seed.txt", "第二次改动\n")
	if err := runner.StashPush(ctx, dir, "第二条贮藏"); err != nil {
		t.Fatalf("第二次贮藏失败: %v", err)
	}

	// 应用 stash@{1}（较早的「第一次改动」），记录本身保留。
	if err := runner.StashApply(ctx, dir, 1); err != nil {
		t.Fatalf("StashApply 失败: %v", err)
	}
	if got := readTestFile(t, dir, "seed.txt"); got != "第一次改动\n" {
		t.Errorf("应用后 seed.txt = %q, 期望 第一次改动", got)
	}
	entries, err := runner.StashList(ctx, dir)
	if err != nil {
		t.Fatalf("StashList 失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("应用后贮藏数量 = %d, 期望 2（应用不删除记录）", len(entries))
	}

	// 把工作区改回原始内容，避免后续应用失败。
	runTestGit(t, dir, "checkout", "--", "seed.txt")
	if got := readTestFile(t, dir, "seed.txt"); got != original {
		t.Fatalf("重置后 seed.txt = %q, 期望 %q", got, original)
	}

	// 删除 stash@{1}，只剩最新一条。
	if err := runner.StashDrop(ctx, dir, 1); err != nil {
		t.Fatalf("StashDrop 失败: %v", err)
	}
	entries, err = runner.StashList(ctx, dir)
	if err != nil {
		t.Fatalf("StashList 失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("删除后贮藏数量 = %d, 期望 1", len(entries))
	}
	if entries[0].Message != "第二条贮藏" {
		t.Errorf("剩余贮藏说明 = %q, 期望 第二条贮藏", entries[0].Message)
	}

	// 应用并删除最新一条。
	if err := runner.StashPopAt(ctx, dir, 0); err != nil {
		t.Fatalf("StashPopAt 失败: %v", err)
	}
	if got := readTestFile(t, dir, "seed.txt"); got != "第二次改动\n" {
		t.Errorf("弹出后 seed.txt = %q, 期望 第二次改动", got)
	}
	entries, err = runner.StashList(ctx, dir)
	if err != nil {
		t.Fatalf("StashList 失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("弹出后贮藏数量 = %d, 期望 0", len(entries))
	}
}

// TestStashIndexValidation 覆盖：负数序号在真正执行 git 之前就被拒绝。
func TestStashIndexValidation(t *testing.T) {
	t.Parallel()
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()
	if err := runner.StashApply(ctx, dir, -1); err == nil {
		t.Error("StashApply 负数序号应当报错")
	}
	if err := runner.StashDrop(ctx, dir, -1); err == nil {
		t.Error("StashDrop 负数序号应当报错")
	}
	if err := runner.StashPopAt(ctx, dir, -1); err == nil {
		t.Error("StashPopAt 负数序号应当报错")
	}
}
