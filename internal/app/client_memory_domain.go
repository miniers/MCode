// 此文件包含 Client 的记忆方法。
package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/localmem"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/store"
	"mcode/internal/textgen"
	mcodeui "mcode/internal/ui"
)

// memoryStoreConn 返回记忆提议用的 store 连接。
// 优先复用已挂载到 MemoryBackend 的持久化 store：与主连接是同一个实例，迁移也只在主连接上跑过，
// 正常运行时不会再出现第二条 sqlite 连接。只有后端未挂载 store（测试或无后端）时才回退到
// 按数据目录单独打开一条连接，此时两个连接指向同一个 sqlite 文件，WAL + busy_timeout(5000)
// 下并发读写是安全的。
func (c *Client) memoryStoreConn() (*store.Store, error) {
	if c.backend != nil {
		if s := c.backend.Store(); s != nil {
			return s, nil
		}
	}
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.memoryStore != nil {
		return c.memoryStore, nil
	}
	dir := c.effectiveDataDir()
	if dir == "" {
		return nil, errors.New("无法定位 mcode 数据目录，记忆提议暂不可用")
	}
	s, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		return nil, fmt.Errorf("打开记忆提议数据库失败: %w", err)
	}
	c.memoryStore = s
	return s, nil
}

// nmemClient 返回懒加载的 Nowledge Mem 客户端；配置损坏时返回明确的中文错误。
func (c *Client) nmemClient() (*nmem.Client, error) {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.memoryNmem != nil {
		return c.memoryNmem, nil
	}
	cl, err := nmem.Default()
	if err != nil {
		return nil, fmt.Errorf("未配置 Nowledge Mem：%w", err)
	}
	c.memoryNmem = cl
	return cl, nil
}

// memorySpace 解析项目对应的 Nowledge 空间：配置覆盖优先，其次按目录同名匹配（memspace 的
// NMEM_SPACE），都没有时落到 default 空间。
func (c *Client) memorySpace(projectPath string) string {
	if cfg, ok := c.loadConfig(); ok {
		if space := strings.TrimSpace(cfg.Memory.SpaceFor(projectPath)); space != "" {
			return space
		}
	}
	for _, kv := range memspace.Env(projectPath) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "NMEM_SPACE" {
			if space := strings.TrimSpace(v); space != "" {
				return space
			}
		}
	}
	return nmem.DefaultSpace
}

// MemoryProposals 列出指定项目的记忆提议（按创建时间倒序）；status 为空表示不过滤。
func (c *Client) MemoryProposals(projectPath, status string) ([]mcodeui.MemoryProposal, error) {
	st, err := c.memoryStoreConn()
	if err != nil {
		return nil, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	items, err := st.ListMemoryProposals(ctx, projectPath, status, 0)
	if err != nil {
		return nil, err
	}
	out := make([]mcodeui.MemoryProposal, 0, len(items))
	for _, p := range items {
		out = append(out, memoryProposalToUI(p))
	}
	return out, nil
}

// CountPendingMemoryProposals 统计指定项目待审核的记忆提议条数。
func (c *Client) CountPendingMemoryProposals(projectPath string) (int, error) {
	st, err := c.memoryStoreConn()
	if err != nil {
		return 0, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	return st.CountPendingMemoryProposals(ctx, projectPath)
}

// UpdateMemoryProposal 保存用户编辑的标题、内容、类型、重要度与写入范围，其余字段（状态、来源、远端 ID、错误）保持不变。
func (c *Client) UpdateMemoryProposal(p mcodeui.MemoryProposal) error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("记忆提议 ID 不能为空")
	}
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("记忆标题不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	cur, err := st.GetMemoryProposal(readCtx, p.ID)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return err
	}
	cur.Title = strings.TrimSpace(p.Title)
	cur.Content = p.Content
	if unitType := strings.TrimSpace(p.UnitType); unitType != "" {
		cur.UnitType = unitType
	}
	cur.Importance = p.Importance
	if p.Scope == store.MemoryProposalScopeProject || p.Scope == store.MemoryProposalScopeGlobal {
		cur.Scope = p.Scope
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	if err := st.UpdateMemoryProposal(ctx, cur); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("记忆提议不存在或已被删除")
		}
		return err
	}
	return nil
}

// SetMemoryProposalTarget 把待审提议设为改写某条已有记忆（合并目标）；targetMemoryID 为空表示取消合并、恢复为新增。
func (c *Client) SetMemoryProposalTarget(id, targetMemoryID string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("记忆提议 ID 不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	cur, err := st.GetMemoryProposal(readCtx, id)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return err
	}
	if cur.Status != store.MemoryProposalStatusPending {
		return errors.New("只能修改待审核的记忆提议")
	}
	cur.TargetMemoryID = strings.TrimSpace(targetMemoryID)
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	if err := st.UpdateMemoryProposal(ctx, cur); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("记忆提议不存在或已被删除")
		}
		return err
	}
	return nil
}

// CondenseMemory 用模型精简一条记忆，返回精简后的标题与正文。
// mergeWith 非空时是要合并进来的已有记忆全文（标题+正文），结果合并两者信息去重后精简。
// projectPath 暂只做保留，不参与请求。
func (c *Client) CondenseMemory(projectPath, title, content, mergeWith string) (string, string, error) {
	_ = projectPath
	if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" {
		return "", "", errors.New("记忆标题和正文不能都为空")
	}

	cfg, ok := c.loadConfig()
	if !ok {
		cfg = config.Defaults()
	}
	provider, model, effort := textgenModelSelection(cfg)
	prompt := textgen.MemoryCondensePrompt(title, content, mergeWith)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx = withTextgenGlobalSelection(ctx, cfg)
	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)
	if err != nil {
		return "", "", err
	}

	newTitle, newContent := textgen.ParseMemoryCondense(out)
	if strings.TrimSpace(newContent) == "" {
		return "", "", errors.New("AI 精简结果为空")
	}
	if strings.TrimSpace(newTitle) == "" {
		newTitle = strings.TrimSpace(title)
	}
	return newTitle, newContent, nil
}

// AcceptMemoryProposal 接受一条待审提议并写入 Nowledge Mem（scope 为 global 时写全局空间，改写提议则更新原记忆）。
// 写入成功时状态置为 accepted 并记下远端 ID；失败时把错误回写到提议上、保持 pending，并把错误返回给界面。
func (c *Client) MemoryBackend() string { return getMemoryBackend() }

func (c *Client) AcceptMemoryProposal(id string) (mcodeui.MemoryProposal, error) {
	if strings.TrimSpace(id) == "" {
		return mcodeui.MemoryProposal{}, errors.New("记忆提议 ID 不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	p, err := st.GetMemoryProposal(readCtx, id)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return mcodeui.MemoryProposal{}, errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	if p.Status != store.MemoryProposalStatusPending {
		return memoryProposalToUI(p), fmt.Errorf("只有待审核的记忆提议才能写入，当前状态为 %s", p.Status)
	}
	if getMemoryBackend() == config.MemoryBackendLocal {
		mem := &localMemoryTools{docs: localmem.New(""), store: st}
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		saved, _, err := mem.commit(ctx, id)
		return memoryProposalToUI(saved), err
	}
	cl, err := c.nmemClient()
	if err != nil {
		return memoryProposalToUI(p), err
	}
	saved, _, err := commitMemoryProposal(context.Background(), st, cl, proposalSpace(p, c.memorySpace(p.ProjectPath)), p)
	return memoryProposalToUI(saved), err
}

// RejectMemoryProposal 驳回一条待审提议：只改本地状态，不碰远端。
func (c *Client) RejectMemoryProposal(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("记忆提议 ID 不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return err
	}
	readCtx, cancelRead := memoryTimeoutCtx()
	p, err := st.GetMemoryProposal(readCtx, id)
	cancelRead()
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("记忆提议不存在或已被删除")
	}
	if err != nil {
		return err
	}
	if p.Status != store.MemoryProposalStatusPending {
		return fmt.Errorf("只有待审核的记忆提议才能驳回，当前状态为 %s", p.Status)
	}
	p.Status = store.MemoryProposalStatusRejected
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	if err := st.UpdateMemoryProposal(ctx, p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("记忆提议不存在或已被删除")
		}
		return err
	}
	return nil
}

// CreateMemoryProposal 用户手动新建一条记忆提议：来源记为 user，状态为 pending，等待本人确认后才写入。
func (c *Client) CreateMemoryProposal(projectPath, threadID, title, content string) (mcodeui.MemoryProposal, error) {
	if strings.TrimSpace(projectPath) == "" {
		return mcodeui.MemoryProposal{}, errors.New("projectPath 不能为空")
	}
	if strings.TrimSpace(title) == "" {
		return mcodeui.MemoryProposal{}, errors.New("记忆标题不能为空")
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	p, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath: projectPath,
		ThreadID:    threadID,
		Title:       strings.TrimSpace(title),
		Content:     content,
		Importance:  0.5,
		Source:      store.MemoryProposalSourceUser,
		Status:      store.MemoryProposalStatusPending,
	})
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	return memoryProposalToUI(p), nil
}

// DistillThreadMemories 从 Nowledge 已同步的会话提炼候选记忆，作为 distill 来源的待审提议放进收件箱，返回新增条数。
func (c *Client) DistillThreadMemories(projectPath, threadID string) (int, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return 0, errors.New("本地记忆后端不支持从会话提炼记忆")
	}
	if strings.TrimSpace(projectPath) == "" {
		return 0, errors.New("projectPath 不能为空")
	}
	if strings.TrimSpace(threadID) == "" {
		return 0, errors.New("会话 ID 不能为空")
	}
	cl, err := c.nmemClient()
	if err != nil {
		return 0, err
	}
	st, err := c.memoryStoreConn()
	if err != nil {
		return 0, err
	}
	space := c.memorySpace(projectPath)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	items, err := cl.DistillPreview(ctx, space, nowledgeThreadIDPrefix+threadID)
	if err != nil {
		if errors.Is(err, nmem.ErrUnsupported) {
			return 0, errors.New("Nowledge 没有找到可提炼的会话：这个会话可能还没同步到 Nowledge（每轮结束后自动同步），或服务端不支持提炼预览")
		}
		return 0, memoryReadError(err)
	}

	existing, err := st.ListMemoryProposals(ctx, projectPath, store.MemoryProposalStatusPending, 0)
	if err != nil {
		return 0, err
	}
	existingTitles := make(map[string]struct{})
	for _, p := range existing {
		if p.ThreadID == threadID {
			existingTitles[p.Title] = struct{}{}
		}
	}

	var added int
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			clean := strings.Join(strings.Fields(item.Content), " ")
			runes := []rune(clean)
			if len(runes) > 40 {
				title = string(runes[:40])
			} else {
				title = string(runes)
			}
		}
		if title == "" {
			continue
		}
		if _, ok := existingTitles[title]; ok {
			continue
		}
		unitType := strings.TrimSpace(item.UnitType)
		if unitType == "" {
			unitType = "fact"
		}
		importance := item.Importance
		if importance <= 0 {
			importance = 0.5
		} else if importance > 1 {
			importance = 1
		}

		if _, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
			ProjectPath: projectPath,
			ThreadID:    threadID,
			Title:       title,
			Content:     item.Content,
			UnitType:    unitType,
			Importance:  importance,
			Labels:      item.Labels,
			Source:      store.MemoryProposalSourceDistill,
			Scope:       store.MemoryProposalScopeProject,
			Status:      store.MemoryProposalStatusPending,
		}); err != nil {
			return added, err
		}
		existingTitles[title] = struct{}{}
		added++
	}
	return added, nil
}

// SearchMemories 在项目对应的空间里语义检索记忆。
func (c *Client) SearchMemories(projectPath, query string) ([]mcodeui.MemoryItem, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("搜索内容不能为空")
	}
	if getMemoryBackend() == config.MemoryBackendLocal {
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		mems, err := localmem.New("").Search(ctx, projectPath, nmem.SearchQuery{Query: query, Limit: 20})
		if err != nil {
			return nil, err
		}
		out := make([]mcodeui.MemoryItem, 0, len(mems))
		for _, m := range mems {
			out = append(out, mcodeui.MemoryItem{ID: m.ID, Title: m.Title, Content: m.Content, UnitType: m.UnitType, Importance: m.Importance, Labels: append([]string(nil), m.Labels...), CreatedAt: m.CreatedAt})
		}
		return out, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	mems, err := searchWithGlobal(ctx, cl, c.memorySpace(projectPath), nmem.SearchQuery{Query: query, Limit: 20})
	if err != nil {
		return nil, memoryReadError(err)
	}
	out := make([]mcodeui.MemoryItem, 0, len(mems))
	for _, m := range mems {
		// 已标记废弃的记忆不再出现在面板搜索结果里。
		if nmem.IsDeprecated(m) {
			continue
		}
		out = append(out, mcodeui.MemoryItem{
			ID:         m.ID,
			Title:      m.Title,
			Content:    m.Content,
			UnitType:   m.UnitType,
			Importance: m.Importance,
			Labels:     append([]string(nil), m.Labels...),
			CreatedAt:  m.CreatedAt,
		})
	}
	return out, nil
}

// ForgetMemory 忘掉一条记忆：服务端支持删除时直接删除，不支持删除时返回 deleted=false，
// 表示已改为给标题打上“已废弃”前缀。成功后一并清掉本地待审里指向这条记忆的提议。
func (c *Client) ForgetMemory(projectPath, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("记忆 ID 不能为空")
	}
	if getMemoryBackend() == config.MemoryBackendLocal {
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		deleted, err := localmem.New("").Forget(ctx, projectPath, id)
		if err != nil {
			return false, err
		}
		c.removePendingProposalsForMemory(ctx, projectPath, id)
		return deleted, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return false, err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	space := c.memorySpace(projectPath)
	deleted, err := cl.ForgetMemory(ctx, space, id)
	if isNmemStatus(err, http.StatusNotFound) && !strings.EqualFold(space, nmem.GlobalSpace) {
		deleted, err = cl.ForgetMemory(ctx, nmem.GlobalSpace, id)
	}
	if err != nil {
		return false, memoryWriteError(err)
	}
	c.removePendingProposalsForMemory(ctx, projectPath, id)
	return deleted, nil
}

// removePendingProposalsForMemory 删除本地待审提议里指向某条远端记忆的条目。
// 失败不影响忘掉记忆本身，所以不返回错误。
func (c *Client) removePendingProposalsForMemory(ctx context.Context, projectPath, memoryID string) int {
	st, err := c.memoryStoreConn()
	if err != nil {
		return 0
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memoryRequestTimeout)
	defer cancel()
	proposals, err := st.ListMemoryProposals(cleanupCtx, projectPath, store.MemoryProposalStatusPending, 0)
	if err != nil {
		return 0
	}
	removed := 0
	for _, p := range proposals {
		if p.TargetMemoryID != memoryID && p.RemoteMemoryID != memoryID {
			continue
		}
		if err := st.DeleteMemoryProposal(cleanupCtx, p.ID); err == nil {
			removed++
		}
	}
	return removed
}

// ExportMemories 把项目空间的全部记忆导出成 Markdown（跳过已废弃的记忆）。
// 开头写导出时间与条数；本地文档后端一次读完（不设条数上限），Nowledge Mem 后端
// 每页 100 条分页读取，最多读 5000 条。
func (c *Client) ExportMemories(projectPath string) (string, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		ctx, cancel := memoryTimeoutCtx()
		defer cancel()
		// 本地文档按小节直接在磁盘上读，不带上限（Search 只对正数 Limit 截断）。
		mems, err := localmem.New("").Search(ctx, projectPath, nmem.SearchQuery{Limit: math.MaxInt32})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n", memoryExportTitle)
		fmt.Fprintf(&b, "空间：本地文档 · 导出时间：%s · 共 %d 条\n",
			time.Now().Format("2006-01-02 15:04"), len(mems))
		for _, scope := range []string{"project", "global"} {
			if scope == "project" {
				b.WriteString("\n## 项目记忆\n\n")
			} else {
				b.WriteString("\n## 全局记忆\n\n")
			}
			for _, m := range mems {
				if m.Space == scope {
					fmt.Fprintf(&b, "### %s\n\n%s\n\n", m.Title, m.Content)
				}
			}
		}
		return b.String(), nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return "", err
	}
	const (
		pageSize    = 100
		maxMemories = 5000
	)
	space := c.memorySpace(projectPath)
	all := make([]nmem.Memory, 0, pageSize)
	for offset := 0; offset < maxMemories; offset += pageSize {
		ctx, cancel := memoryTimeoutCtx()
		page, err := cl.ListMemories(ctx, space, pageSize, offset)
		cancel()
		if err != nil {
			return "", memoryReadError(err)
		}
		for _, m := range page {
			if nmem.IsDeprecated(m) {
				continue
			}
			all = append(all, m)
		}
		if len(page) < pageSize {
			break
		}
	}
	return renderMemoryExport(space, time.Now(), all), nil
}

// WorkingMemory 读取项目对应空间的今日 Working Memory 正文。
func (c *Client) WorkingMemory(projectPath string) (string, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return "", nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return "", err
	}
	ctx, cancel := memoryTimeoutCtx()
	defer cancel()
	text, err := cl.WorkingMemory(ctx, c.memorySpace(projectPath))
	if err != nil {
		return "", memoryReadError(err)
	}
	return text, nil
}

// MemorySpaces 列出 Nowledge Mem 中所有记忆空间，供设置页的项目记忆空间下拉框使用。
func (c *Client) MemorySpaces(ctx context.Context) ([]mcodeui.MemorySpace, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return []mcodeui.MemorySpace{}, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := memoryCallCtx(ctx)
	defer cancel()
	spaces, err := cl.Spaces(callCtx)
	if err != nil {
		return nil, memoryReadError(err)
	}
	out := make([]mcodeui.MemorySpace, 0, len(spaces))
	for _, s := range spaces {
		out = append(out, mcodeui.MemorySpace{ID: s.ID, Name: s.Name})
	}
	return out, nil
}

// ResolveMemorySpace 解析项目路径对应的记忆空间：override 非空时按它匹配，
// 否则按仓库目录同名匹配（与子进程注入 NMEM_SPACE 的规则一致）。
func (c *Client) ResolveMemorySpace(ctx context.Context, projectPath, override string) (mcodeui.MemorySpaceResolution, error) {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return mcodeui.MemorySpaceResolution{}, nil
	}
	cl, err := c.nmemClient()
	if err != nil {
		return mcodeui.MemorySpaceResolution{}, err
	}
	callCtx, cancel := memoryCallCtx(ctx)
	defer cancel()
	res := memspace.Describe(callCtx, cl, projectPath, override)
	return mcodeui.MemorySpaceResolution{
		Space:        res.Space,
		FromOverride: res.FromOverride,
		Hit:          res.Hit,
	}, nil
}
