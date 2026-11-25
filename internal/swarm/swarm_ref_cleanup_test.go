package swarm

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestSwarmRefCleanup_LandRefRemovedAfterLanding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("landBeeCommit落地后删除swarm-land引用", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const lead = "land1111-lead"
		initLeadForCommitLanding(t, m, root, lead)

		bee := startCommitLandingBee(t, m, root, lead, 0)
		writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))

		if !landCommitBee(m, root, lead, &bee) {
			t.Fatal("landCommitBee 不应等待")
		}
		assertLandingCommit(t, m, root, bee)

		refs, err := m.opts.Git.ListRefs(ctx, root, "refs/mcode/swarm-land/")
		if err != nil {
			t.Fatalf("ListRefs failed: %v", err)
		}
		if len(refs) != 0 {
			t.Fatalf("落地后不应残留 swarm-land 引用: %+v", refs)
		}
	})

	t.Run("integratePatch返回后删除swarm-land引用", func(t *testing.T) {
		t.Parallel()
		m, _, root := newCommitLandingManager(t)
		const lead = "intg2222-lead"
		initLeadForCommitLanding(t, m, root, lead)

		bee := startCommitLandingBee(t, m, root, lead, 0)
		writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
		if err := m.opts.Git.AddAll(ctx, bee.WorktreePath); err != nil {
			t.Fatalf("AddAll failed: %v", err)
		}

		integrated, err := m.integratePatch(ctx, lead, root, &bee)
		if err != nil {
			t.Fatalf("integratePatch failed: %v", err)
		}
		if len(integrated.patch) == 0 {
			t.Fatal("预期生成非空补丁")
		}

		refs, err := m.opts.Git.ListRefs(ctx, root, "refs/mcode/swarm-land/")
		if err != nil {
			t.Fatalf("ListRefs failed: %v", err)
		}
		if len(refs) != 0 {
			t.Fatalf("integratePatch 返回后不应残留 swarm-land 引用: %+v", refs)
		}
	})
}

func TestSwarmRefCleanup_ClearFinished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, _, root := newCommitLandingManager(t)

	const leadDone = "done1111-lead"
	const leadActive = "actv2222-lead"
	initLeadForCommitLanding(t, m, root, leadDone)
	initLeadForCommitLanding(t, m, root, leadActive)

	b0 := startCommitLandingBee(t, m, root, leadDone, 0)
	b1 := startCommitLandingBee(t, m, root, leadDone, 1)
	writePipelineFile(t, b0.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
	writePipelineFile(t, b1.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))

	if !landCommitBee(m, root, leadDone, &b0) || !landCommitBee(m, root, leadDone, &b1) {
		t.Fatal("leadDone 落地不应等待")
	}

	head := gitTestRun(t, root, "rev-parse", "HEAD")
	donePrefix := leadPrefixID(leadDone)
	activePrefix := leadPrefixID(leadActive)
	for _, ref := range []string{
		fmt.Sprintf("refs/mcode/swarm-base/%s", donePrefix),
		fmt.Sprintf("refs/mcode/swarm-base/%s", activePrefix),
		fmt.Sprintf("refs/mcode/swarm-fold/%s/0", activePrefix),
	} {
		if err := m.opts.Git.UpdateRef(ctx, root, ref, head); err != nil {
			t.Fatalf("UpdateRef %s: %v", ref, err)
		}
	}

	// 确认清理前 done lead 的 swarm-base 和 swarm-fold 都存在。
	if refs, err := m.opts.Git.ListRefs(ctx, root, "refs/mcode/swarm-fold/"+donePrefix+"/"); err != nil || len(refs) == 0 {
		t.Fatalf("清理前应存在 swarm-fold 引用, err=%v refs=%+v", err, refs)
	}

	m.mu.Lock()
	for i := range m.leads[leadDone].bees {
		m.leads[leadDone].bees[i].Status = StatusCompleted
		m.leads[leadDone].bees[i].Reported = true
	}
	m.leads[leadActive].bees = append(m.leads[leadActive].bees, Bee{
		ThreadID:      "active-bee-1",
		Status:        StatusRunning,
		TargetWorkDir: root,
	})
	m.mu.Unlock()

	if err := m.ClearFinished(ctx, leadDone); err != nil {
		t.Fatalf("ClearFinished failed: %v", err)
	}

	for _, deletedPrefix := range []string{
		"refs/mcode/swarm-base/" + donePrefix,
		"refs/mcode/swarm-fold/" + donePrefix + "/",
		"refs/mcode/swarm-land/" + donePrefix + "/",
	} {
		refs, err := m.opts.Git.ListRefs(ctx, root, deletedPrefix)
		if err != nil {
			t.Fatalf("ListRefs %s failed: %v", deletedPrefix, err)
		}
		if len(refs) != 0 {
			t.Errorf("ClearFinished 后仍残留引用 %s: %+v", deletedPrefix, refs)
		}
	}

	for _, keptRef := range []string{
		"refs/mcode/swarm-base/" + activePrefix,
		"refs/mcode/swarm-fold/" + activePrefix + "/0",
	} {
		if got := gitTestRun(t, root, "rev-parse", keptRef); got != head {
			t.Errorf("活跃 lead 引用 %s 被误删: got=%s want=%s", keptRef, got, head)
		}
	}
}

func TestSwarmRefCleanup_DeleteLead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, _, root := newCommitLandingManager(t)

	const leadDelete = "del11111-lead"
	const leadKeep = "keep2222-lead"
	initLeadForCommitLanding(t, m, root, leadDelete)
	initLeadForCommitLanding(t, m, root, leadKeep)

	b := startCommitLandingBee(t, m, root, leadDelete, 0)
	b.Merged = true
	m.mu.Lock()
	m.leads[leadDelete].bees[0].Merged = true
	m.leads[leadKeep].bees = append(m.leads[leadKeep].bees, Bee{
		ThreadID:      "keep-bee-1",
		Status:        StatusRunning,
		TargetWorkDir: root,
	})
	m.mu.Unlock()

	head := gitTestRun(t, root, "rev-parse", "HEAD")
	delPrefix := leadPrefixID(leadDelete)
	keepPrefix := leadPrefixID(leadKeep)
	for _, ref := range []string{
		"refs/mcode/swarm-base/" + delPrefix,
		"refs/mcode/swarm-fold/" + delPrefix + "/0",
		"refs/mcode/swarm-land/" + delPrefix + "/0",
		"refs/mcode/swarm-base/" + keepPrefix,
		"refs/mcode/swarm-fold/" + keepPrefix + "/0",
	} {
		if err := m.opts.Git.UpdateRef(ctx, root, ref, head); err != nil {
			t.Fatalf("UpdateRef %s: %v", ref, err)
		}
	}

	m.BeeThreadDeleted(ctx, leadDelete)

	for _, deletedPrefix := range []string{
		"refs/mcode/swarm-base/" + delPrefix,
		"refs/mcode/swarm-fold/" + delPrefix + "/",
		"refs/mcode/swarm-land/" + delPrefix + "/",
	} {
		refs, err := m.opts.Git.ListRefs(ctx, root, deletedPrefix)
		if err != nil {
			t.Fatalf("ListRefs %s failed: %v", deletedPrefix, err)
		}
		if len(refs) != 0 {
			t.Errorf("BeeThreadDeleted 后仍残留引用 %s: %+v", deletedPrefix, refs)
		}
	}

	for _, keptRef := range []string{
		"refs/mcode/swarm-base/" + keepPrefix,
		"refs/mcode/swarm-fold/" + keepPrefix + "/0",
	} {
		if got := gitTestRun(t, root, "rev-parse", keptRef); got != head {
			t.Errorf("活跃 lead 引用 %s 被误删: got=%s want=%s", keptRef, got, head)
		}
	}
}

func TestSwarmRefCleanup_SweepOrphanCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, root := initTestRepo(t)

	writePipelineFile(t, root, "a.txt", "a\n")
	gitTestRun(t, root, "add", "a.txt")
	gitTestRun(t, root, "commit", "-qm", "init")
	head := gitTestRun(t, root, "rev-parse", "HEAD")

	m := NewManager(Options{Git: runner})
	const activeLead = "actv1111-lead"
	const queuedLead = "queu2222-lead"
	const emptyLead = "empt3333-lead"
	const orphanPrefix = "orph9999"

	m.mu.Lock()
	m.leads[activeLead] = &leadState{
		leadThreadID: activeLead,
		leadWorkDir:  root,
		bees: []Bee{{
			ThreadID:      "active-bee",
			Status:        StatusCompleted,
			PendingMerge:  true,
			TargetWorkDir: root,
		}},
	}
	m.leads[emptyLead] = &leadState{
		leadThreadID: emptyLead,
		leadWorkDir:  root,
	}
	m.queue = append(m.queue, queuedBee{
		leadThreadID: queuedLead,
		leadWorkDir:  root,
		spec:         BeeSpec{Name: "QueuedBee"},
	})
	m.mu.Unlock()

	createTriplet := func(prefix string) []string {
		return []string{
			"refs/mcode/swarm-base/" + prefix,
			"refs/mcode/swarm-land/" + prefix + "/0",
			"refs/mcode/swarm-fold/" + prefix + "/0",
		}
	}
	keptRefs := append(createTriplet(leadPrefixID(activeLead)), createTriplet(leadPrefixID(queuedLead))...)
	keptRefs = append(keptRefs, "refs/mcode/checkpoints/other/turn-1")
	orphanRefs := append(createTriplet(leadPrefixID(emptyLead)), createTriplet(orphanPrefix)...)

	for _, ref := range append(append([]string{}, keptRefs...), orphanRefs...) {
		if err := runner.UpdateRef(ctx, root, ref, head); err != nil {
			t.Fatalf("UpdateRef %s: %v", ref, err)
		}
	}

	if _, err := m.SweepOrphanCopies(ctx); err != nil {
		t.Fatalf("SweepOrphanCopies failed: %v", err)
	}

	allRefs, err := runner.ListRefs(ctx, root, "refs/")
	if err != nil {
		t.Fatalf("ListRefs failed: %v", err)
	}
	present := make(map[string]bool, len(allRefs))
	for _, r := range allRefs {
		present[r.Name] = true
	}

	for _, ref := range orphanRefs {
		if present[ref] {
			t.Errorf("孤立引用未被清扫: %s", ref)
		}
	}
	for _, ref := range keptRefs {
		if !present[ref] {
			t.Errorf("活跃 lead 或无关引用被误删: %s", ref)
		}
	}
}
