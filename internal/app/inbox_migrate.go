package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"mcode/internal/projctx"
	"mcode/internal/store"
)

// legacyInboxMigrateTimeout 是启动时旧收件箱迁移的整体上限。
const legacyInboxMigrateTimeout = 10 * time.Minute

// migrateLegacyInboxOnStartup 在启动后把旧收件箱的备忘迁成待审提议；ctx 取消时立即退出。
func migrateLegacyInboxOnStartup(ctx context.Context, st *store.Store) {
	ctx, cancel := context.WithTimeout(ctx, legacyInboxMigrateTimeout)
	defer cancel()
	projects, err := st.ListProjects(ctx)
	if err != nil {
		log.Printf("旧收件箱迁移失败: %v", err)
		return
	}
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		paths = append(paths, project.Path)
	}
	n, err := migrateLegacyInboxMemos(ctx, st, paths)
	if n > 0 {
		log.Printf("旧收件箱已迁移 %d 条备忘", n)
	}
	if err != nil {
		log.Printf("旧收件箱迁移失败: %v", err)
	}
}

// migrateLegacyInboxMemos 把各项目收件箱里的备忘转成待审提议并删除原备忘。
//
// 每次运行先读该项目已有的 inbox-migration 提议，按标题 + 正文去重：已经迁移过的备忘
// 不再重复建提议，只重试删除原备忘，因此「建提议成功、删备忘失败」后重跑不会产生重复提议。
func migrateLegacyInboxMemos(ctx context.Context, st *store.Store, projectPaths []string) (migrated int, err error) {
	for _, path := range projectPaths {
		if e := ctx.Err(); e != nil {
			return migrated, e
		}
		svc, e := projctx.New(path)
		if e != nil {
			err = errors.Join(err, fmt.Errorf("项目 %s: %w", path, e))
			continue
		}
		existing, e := migratedInboxKeys(ctx, st, path)
		if e != nil {
			err = errors.Join(err, fmt.Errorf("项目 %s: %w", path, e))
			continue
		}
		items, e := svc.ListInbox()
		if e != nil {
			err = errors.Join(err, fmt.Errorf("项目 %s: %w", path, e))
			continue
		}
		for _, item := range items {
			if e := ctx.Err(); e != nil {
				return migrated, e
			}
			title := item.Title()
			for _, line := range strings.Split(item.Content, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "#") {
					if heading := strings.TrimSpace(strings.TrimLeft(line, "#")); heading != "" {
						title = heading
						break
					}
				}
			}
			if strings.TrimSpace(title) == "" {
				title = item.Name
			}
			key := inboxMigrationKey(title, item.Content)
			if !existing[key] {
				_, e = st.CreateMemoryProposal(ctx, store.MemoryProposal{
					ProjectPath: path, Title: title, Content: item.Content, UnitType: "learning",
					Source: "inbox-migration", Scope: store.MemoryProposalScopeProject, Status: store.MemoryProposalStatusPending,
				})
				if e != nil {
					err = errors.Join(err, fmt.Errorf("迁移 %s/%s: %w", path, item.Name, e))
					continue
				}
				existing[key] = true
				migrated++
			}
			if e = svc.DiscardInbox(item.Name); e != nil {
				err = errors.Join(err, fmt.Errorf("删除 %s/%s: %w", path, item.Name, e))
			}
		}
	}
	return migrated, err
}

// migratedInboxKeys 返回该项目已迁移过的备忘键（标题 + 正文），只看来源为 inbox-migration、
// 不限状态的提议。status 传空表示不过滤状态，limit 传 0 表示不分页、取全部记录。
func migratedInboxKeys(ctx context.Context, st *store.Store, path string) (map[string]bool, error) {
	proposals, err := st.ListMemoryProposals(ctx, path, "", 0)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(proposals))
	for _, p := range proposals {
		if p.Source != "inbox-migration" {
			continue
		}
		keys[inboxMigrationKey(p.Title, p.Content)] = true
	}
	return keys, nil
}

// inboxMigrationKey 组合标题与正文作为去重键；两者都相同才视为同一条备忘。
func inboxMigrationKey(title, content string) string {
	return title + "\x00" + content
}
