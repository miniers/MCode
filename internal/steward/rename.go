package steward

import (
	"context"
	"log"
	"strings"

	"mcode/internal/config"
)

// Names 是总管的可选改名能力：读取并改写总管主会话的标题与主目录对应项目的名称。
//
// 用户在设置里给总管改名后，旧会话的标题与总管家目录的项目名仍是旧名字，
// 界面标题栏、搜索与项目选择里会一直显示旧名。Ensure 复用已有会话时靠本能力把它们改成
// 当前配置名。由 app 层用会话后端接线；未接线（Deps.Names 为空）时总管跳过名字同步，
// 只在新建会话时用当时的配置名当标题。
type Names interface {
	// ThreadTitle 返回会话当前标题，读不到时返回错误。
	ThreadTitle(ctx context.Context, threadID string) (string, error)
	// RenameThread 按会话 ID 改会话标题。
	RenameThread(ctx context.Context, threadID, title string) error
	// HomeProjectName 返回主目录对应项目的名称；没有对应项目时返回空串。
	HomeProjectName(ctx context.Context, dir string) (string, error)
	// RenameHomeProject 把主目录对应项目改名；没有对应项目时什么也不做。
	RenameHomeProject(ctx context.Context, dir, name string) error
}

// syncNames 让总管主会话的标题与家目录项目名跟上当前配置名。
// 只处理总管主会话与总管家目录项目，不碰话题会话和其他项目。
// 失败只记日志：总管照样可用，下一次 Ensure 会再试。
func (r *Runtime) syncNames(ctx context.Context, cfg config.StewardConfig, threadID string) {
	if r.names == nil {
		return
	}
	name := cfg.Label()
	if strings.TrimSpace(threadID) != "" {
		r.syncThreadTitle(ctx, threadID, name)
	}
	r.syncHomeProjectName(ctx, cfg.ResolvedHomeDir(r.dataDir), name)
}

// syncThreadTitle 把总管会话标题改成 name。
// 先读当前标题比较，名字没变就不写库；读不到时用进程内记住的上次同步结果判断，
// 避免每次唤醒都写一次。
func (r *Runtime) syncThreadTitle(ctx context.Context, threadID, name string) {
	current, err := r.names.ThreadTitle(ctx, threadID)
	if err != nil {
		log.Printf("steward: 读取总管会话标题失败，按上次同步结果判断: %v", err)
		if r.rememberedThreadName(threadID) == name {
			return
		}
	} else if strings.TrimSpace(current) == name {
		r.rememberThreadName(threadID, name)
		return
	}
	if err := r.names.RenameThread(ctx, threadID, name); err != nil {
		log.Printf("steward: 更新总管会话标题失败: %v", err)
		return
	}
	r.rememberThreadName(threadID, name)
}

// syncHomeProjectName 把总管主目录对应的项目名改成 name。
// 没有对应项目时什么也不做：项目名同步不负责建项目，建项目仍由 EnsureHomeProject 负责。
func (r *Runtime) syncHomeProjectName(ctx context.Context, dir, name string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	current, err := r.names.HomeProjectName(ctx, dir)
	if err != nil {
		log.Printf("steward: 读取总管项目名失败，按上次同步结果判断: %v", err)
		if r.rememberedProjectName(dir) == name {
			return
		}
	} else if current == "" {
		// 总管家目录还没有登记成项目：建项目仍由 EnsureHomeProject 负责。
		return
	} else if strings.TrimSpace(current) == name {
		r.rememberProjectName(dir, name)
		return
	}
	if err := r.names.RenameHomeProject(ctx, dir, name); err != nil {
		log.Printf("steward: 更新总管项目名失败: %v", err)
		return
	}
	r.rememberProjectName(dir, name)
}

// rememberThreadName 记下本进程已同步成功的会话标题。
func (r *Runtime) rememberThreadName(threadID, name string) {
	r.mu.Lock()
	r.syncedThreadID = threadID
	r.syncedThreadName = name
	r.mu.Unlock()
}

// rememberedThreadName 返回本进程上次给该会话同步成功的标题；没同步过时为空。
func (r *Runtime) rememberedThreadName(threadID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.syncedThreadID != threadID {
		return ""
	}
	return r.syncedThreadName
}

// rememberProjectName 记下本进程已同步成功的项目名。
func (r *Runtime) rememberProjectName(dir, name string) {
	r.mu.Lock()
	r.syncedProjectDir = dir
	r.syncedProjectName = name
	r.mu.Unlock()
}

// rememberedProjectName 返回本进程上次给该目录项目同步成功的名字；没同步过时为空。
func (r *Runtime) rememberedProjectName(dir string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.syncedProjectDir != dir {
		return ""
	}
	return r.syncedProjectName
}
