package mcodeui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// githubReposLimit 是 gh repo list 拉取的仓库数量上限。
const githubReposLimit = 50

// GitHub 克隆面板的卡片宽度与仓库列表高度。
const (
	githubCloneCardWidth   float32 = 460
	githubCloneReposHeight float32 = 220
)

// githubCloneResult 是后台任务（gh 列仓库、目录选择、git clone）回传给 UI 线程的结果，
// 界面状态只在 UI 线程写入，后台只投递结果。
type githubCloneResult struct {
	kind     string // "repos"、"dir"、"cloned"、"error"
	repos    []string
	dir      string
	fullName string
	project  Project
	err      error
}

// githubCloneState 是 GitHub 克隆面板的界面状态，仅 UI 线程读写；
// 后台 goroutine 通过 results 通道回传结果。
type githubCloneState struct {
	open        bool
	search      string // 输入框：Git URL 或 owner/repo
	parentDir   string // 克隆到的父目录
	repos       []string
	reposLoaded bool
	reposErr    string
	cloning     bool
	cloningName string
	errMsg      string
	results     chan githubCloneResult
}

// getGitHubCloneState 返回 chatState 上的克隆面板状态。
func getGitHubCloneState(s *chatState) *githubCloneState {
	st := getChatUIState(s)
	if st == nil {
		return nil
	}
	return &st.clone
}

// openGitHubClonePanel 打开克隆面板（侧边栏入口、空态项目浮层入口与测试共用）。
func openGitHubClonePanel(s *chatState) {
	cs := getGitHubCloneState(s)
	if cs == nil {
		return
	}
	cs.open = true
	cs.errMsg = ""
	if !cs.reposLoaded {
		loadGitHubRepos(s, cs)
	}
	s.invalidate()
}

// closeGitHubClonePanel 关闭克隆面板。
func closeGitHubClonePanel(s *chatState) {
	cs := getGitHubCloneState(s)
	if cs == nil {
		return
	}
	cs.open = false
	cs.errMsg = ""
	s.invalidate()
}

// validateGitHubSegment 校验 owner 或仓库名只含 GitHub 允许的字符。
func validateGitHubSegment(seg string) bool {
	if seg == "" {
		return false
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// parseGitHubRepo 解析用户输入的 Git URL 或 owner/repo，返回规范的 owner/repo。
// 支持 git@github.com:owner/repo.git、ssh://git@github.com/owner/repo.git、
// https://github.com/owner/repo(.git)、github.com/owner/repo 以及 owner/repo。
func parseGitHubRepo(input string) (string, bool) {
	path := strings.TrimSpace(input)
	if path == "" {
		return "", false
	}
	allowsExtraPath := false
	switch {
	case strings.HasPrefix(path, "git@github.com:"):
		path = strings.TrimPrefix(path, "git@github.com:")
	case strings.HasPrefix(path, "ssh://git@github.com/"):
		path = strings.TrimPrefix(path, "ssh://git@github.com/")
		allowsExtraPath = true
	case strings.HasPrefix(path, "https://"):
		rest := strings.TrimPrefix(path, "https://")
		host, tail, ok := strings.Cut(rest, "/")
		if !ok || host != "github.com" {
			return "", false
		}
		path, allowsExtraPath = tail, true
	case strings.HasPrefix(path, "http://"):
		rest := strings.TrimPrefix(path, "http://")
		host, tail, ok := strings.Cut(rest, "/")
		if !ok || host != "github.com" {
			return "", false
		}
		path, allowsExtraPath = tail, true
	case strings.HasPrefix(path, "github.com/"):
		path = strings.TrimPrefix(path, "github.com/")
		allowsExtraPath = true
	}
	// 去掉查询串与锚点，避免把 ?tab=readme 之类当成路径。
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	segments := make([]string, 0, 2)
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	if len(segments) < 2 {
		return "", false
	}
	if len(segments) > 2 && !allowsExtraPath {
		return "", false
	}
	owner := segments[0]
	repo := strings.TrimSuffix(segments[1], ".git")
	if !validateGitHubSegment(owner) || !validateGitHubSegment(repo) {
		return "", false
	}
	return owner + "/" + repo, true
}

// githubCloneURL 把 owner/repo 转换为 https 克隆地址。
func githubCloneURL(fullName string) string {
	return "https://github.com/" + fullName + ".git"
}

// repoDirName 从 Git URL 或 owner/repo 提取克隆目录名（仓库名）。
func repoDirName(input string) string {
	if fullName, ok := parseGitHubRepo(input); ok {
		return fullName[strings.Index(fullName, "/")+1:]
	}
	trimmed := strings.TrimSpace(input)
	if i := strings.IndexAny(trimmed, "?#"); i >= 0 {
		trimmed = trimmed[:i]
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		trimmed = trimmed[i+1:]
	} else if i := strings.LastIndex(trimmed, ":"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return trimmed
}

// cloneTargetDir 拼接克隆目标目录：父目录 + 仓库名。
func cloneTargetDir(parentDir, input string) string {
	name := repoDirName(input)
	if name == "" {
		return ""
	}
	return filepath.Join(parentDir, name)
}

// cloneNameFromInput 返回用于展示与解析的规范 owner/repo，解析失败时返回去掉空白的原文。
func cloneNameFromInput(input string) string {
	if fullName, ok := parseGitHubRepo(input); ok {
		return fullName
	}
	return strings.TrimSpace(input)
}

// listGitHubRepos 调用本机 gh 命令列出仓库（gh repo list --limit 50 --json nameWithOwner）。
// 未安装 gh 或命令失败时返回错误，由界面提示用户直接粘贴 URL。
func listGitHubRepos(ctx context.Context) ([]string, error) {
	// GUI 进程的 PATH 不带登录 Shell 的目录，用登录环境查找 gh。
	ghPath, err := loginenv.LookPath("gh")
	if err != nil {
		return nil, errors.New("未找到 gh 命令")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, ghPath, "repo", "list", "--limit", "50", "--json", "nameWithOwner"))
	// gh 需要登录环境里的配置与凭据环境变量。
	cmd.Env = loginenv.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var parsed []struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	repos := make([]string, 0, len(parsed))
	for _, r := range parsed {
		if r.NameWithOwner != "" {
			repos = append(repos, r.NameWithOwner)
		}
	}
	return repos, nil
}

// runGitClone 执行 git clone <url> <dir>（os/exec，不引入 cgo）。
func runGitClone(ctx context.Context, url, dir string) error {
	// GUI 进程的 PATH 不带登录 Shell 的目录，用登录环境查找 git。
	gitPath, err := loginenv.LookPath("git")
	if err != nil {
		return errors.New("未找到 git 命令")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, gitPath, "clone", url, dir))
	// git 依赖登录环境里的 GIT_* 变量与 credentials 配置。
	cmd.Env = loginenv.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
				msg = msg[:idx]
			}
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// loadGitHubRepos 在后台用 gh 列出仓库，结果通过 results 通道回到 UI 线程。
func loadGitHubRepos(s *chatState, cs *githubCloneState) {
	if cs == nil || cs.reposLoaded {
		return
	}
	cs.reposLoaded = true
	results := cs.results
	go func() {
		repos, err := listGitHubRepos(context.Background())
		results <- githubCloneResult{kind: "repos", repos: repos, err: err}
		s.invalidate()
	}()
}

// pickGitHubCloneDir 在后台弹出目录选择框，结果通过 results 通道回到 UI 线程。
// 复用 promptAddProject 的选择器注入方式（WithPickFolder）。
func pickGitHubCloneDir(s *chatState, cs *githubCloneState) {
	if cs == nil {
		return
	}
	s.mu.Lock()
	pickFn := s.pickFolder
	s.mu.Unlock()

	results := cs.results
	go func() {
		var (
			path string
			ok   bool
			err  error
		)
		switch {
		case pickFn != nil:
			path, ok = pickFn()
		case pickFolderDialog != nil:
			path, ok, err = pickFolderDialog()
		default:
			path, ok, err = defaultPickFolder()
		}
		if err != nil {
			results <- githubCloneResult{kind: "error", err: err}
		} else if ok && path != "" {
			results <- githubCloneResult{kind: "dir", dir: path}
		}
		s.invalidate()
	}()
}

// startGitHubClone 校验输入并启动后台克隆：git clone 成功后把目录添加为项目并切换过去。
func startGitHubClone(s *chatState, cs *githubCloneState) {
	if cs == nil || cs.cloning {
		return
	}
	fullName, ok := parseGitHubRepo(cs.search)
	if !ok {
		cs.errMsg = "请输入 Git URL 或 owner/repo"
		s.invalidate()
		return
	}
	parentDir := strings.TrimSpace(cs.parentDir)
	if parentDir == "" {
		cs.errMsg = "请选择克隆到的目录"
		s.invalidate()
		return
	}
	dir := cloneTargetDir(parentDir, fullName)
	if dir == "" {
		cs.errMsg = "无法解析仓库名"
		s.invalidate()
		return
	}
	if _, err := os.Stat(dir); err == nil {
		cs.errMsg = "目标目录已存在：" + dir
		s.invalidate()
		return
	}

	cs.cloning = true
	cs.cloningName = fullName
	cs.errMsg = ""
	results := cs.results
	s.invalidate()

	go func() {
		ctx := context.Background()
		if err := runGitClone(ctx, githubCloneURL(fullName), dir); err != nil {
			results <- githubCloneResult{kind: "error", err: err, fullName: fullName}
			s.invalidate()
			return
		}
		proj, err := s.client.AddProject(ctx, dir)
		if err != nil {
			results <- githubCloneResult{kind: "error", err: err, fullName: fullName, dir: dir}
			s.invalidate()
			return
		}
		results <- githubCloneResult{kind: "cloned", dir: dir, fullName: fullName, project: proj}
		s.invalidate()
	}()
}

// drainGitHubCloneResults 在渲染时消费后台结果，保证界面状态只在 UI 线程更新。
func drainGitHubCloneResults(s *chatState, cs *githubCloneState) {
	if cs == nil {
		return
	}
	// 克隆完成后需要按项目名切换，这里缓存 AddProject 的返回值。
	for {
		select {
		case res := <-cs.results:
			switch res.kind {
			case "repos":
				if res.err != nil {
					cs.reposErr = res.err.Error()
				} else {
					cs.reposErr = ""
					cs.repos = res.repos
				}
			case "dir":
				cs.parentDir = res.dir
			case "cloned":
				cs.cloning = false
				cs.cloningName = ""
				cs.open = false
				cs.search = ""
				cs.errMsg = ""
				cs.parentDir = res.dir
				// 添加项目并切换到该项目下的新会话
				s.loadProjects()
				if res.project.ID != "" {
					s.createThread(res.project.ID)
				}
				s.setToast("已克隆 " + res.fullName)
			case "error":
				cs.cloning = false
				cs.cloningName = ""
				cs.errMsg = "克隆失败: " + res.err.Error()
			}
		default:
			return
		}
	}
}

// renderGitHubClonePanel 渲染 GitHub 克隆面板：
// 居中不透明浮动卡片（宽 460），支持粘贴 Git URL 或从 gh 仓库列表选择，选择目录后克隆。
func renderGitHubClonePanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	cs := getGitHubCloneState(s)
	if cs == nil {
		return
	}
	drainGitHubCloneResults(s, cs)
	if !cs.open {
		modalDragReset(modalDragGitHubClone)
		return
	}

	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragGitHubClone, githubCloneCardWidth, 500, 360, 240, winW, winH)
	modalDragModal(c, modalDragGitHubClone, &cs.open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			closeGitHubClonePanel(s)
			return
		}

		ui.Column(c.Key("github-clone-card")).
			Width(width).
			Height(height).
			Background(ThemeSurface(c)).
			Border(Hairline, ThemeBorder(c)).
			Radius(RadiusXL).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行：GitHub 图标 + 标题说明 + 关闭按钮，整行兼作弹窗的拖拽把手
				modalDragHandle(c, modalDragGitHubClone, ui.Row(c.Key("github-clone-header")).
					FillWidth().
					Gap(Space3).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, IconGitFork()).Size(IconMD, IconMD).TextColor(ThemePrimaryText(c))
						ui.Column(c.Key("github-clone-title-col")).
							Grow(1).
							Gap(Space0).
							Children(func() {
								ui.Text(c, "从 GitHub 克隆").
									FontSize(uiFont(TypeProse)).
									FontWeight(600).
									TextColor(ThemePrimaryText(c)).
									SingleLine()
								ui.Text(c, "选择仓库，克隆到你指定的目录并作为项目打开。").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							})
						IconButton(c.Key("github-clone-close-btn"), IconButtonProps{
							Icon:  IconX(),
							Label: "关闭",
							OnClick: func() {
								closeGitHubClonePanel(s)
							},
							Variant: ButtonGhost,
							Size:    ControlMD,
						})
					}))

				// 输入框：粘贴 Git URL 或输入 owner/repo，也可用于过滤 gh 仓库列表
				in := ui.TextInput(c.Key("github-clone-search"), &cs.search).
					Placeholder("搜索仓库或粘贴 Git URL").
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					Background(ThemeHover(c).Alpha(0.25)).
					BorderColor(ThemeBorder(c)).
					BorderWidth(Hairline).
					Radius(RadiusMD).
					Padding(Space3, Space3).
					FillWidth().
					AutoFocus()
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter {
						startGitHubClone(s, cs)
						return true
					}
					return false
				})

				// 仓库候选列表（gh 可用时），高度固定，避免面板跳动
				ui.Column(c.Key("github-clone-repos")).
					FillWidth().
					Grow(1).
					MinHeight(0).
					BorderWidth(Hairline).
					BorderColor(ThemeBorder(c)).
					Radius(RadiusMD).
					Clip().
					Children(func() {
						ui.Scroll(c.Key("github-clone-repos-scroll")).Fill().Children(func() {
							ui.Column(c.Key("github-clone-repos-col")).FillWidth().Padding(Space1).Gap(Space0).Children(func() {
								query := strings.TrimSpace(cs.search)
								hasPasted, _ := parseGitHubRepo(query)
								matches := 0
								if hasPasted != "" {
									renderGitHubRepoRow(c, s, cs, hasPasted, "克隆 "+hasPasted)
									matches++
								}
								for _, repo := range cs.repos {
									if query != "" && !strings.Contains(strings.ToLower(repo), strings.ToLower(query)) {
										continue
									}
									if repo == hasPasted {
										continue
									}
									renderGitHubRepoRow(c, s, cs, repo, repo)
									matches++
								}
								if matches == 0 {
									hint := "粘贴 Git URL 或输入 owner/repo 后按回车克隆"
									if cs.reposErr != "" {
										hint = cs.reposErr + "，可直接粘贴 URL"
									}
									ui.Text(c, hint).
										FontSize(uiFont(TypeFootnote)).
										TextColor(ThemeSecondaryText(c)).
										Padding(Space3)
								}
							})
						})
					})

				// 克隆目标目录 + 底部操作
				ui.Row(c.Key("github-clone-footer")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Column(c.Key("github-clone-dir-col")).
							Grow(1).
							Gap(Space0).
							Children(func() {
								ui.Text(c, "克隆到").
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemePrimaryText(c))
								dirText := cs.parentDir
								if dirText == "" {
									dirText = "尚未选择目录"
								}
								ui.Text(c, dirText).
									FontSize(uiFont(TypeCaption)).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							})
						Button(c.Key("github-clone-dir-btn"), ButtonOpts{
							Text: "更改…",
							OnClick: func() {
								pickGitHubCloneDir(s, cs)
							},
							Variant: ButtonSecondary,
						})
					})

				ui.Row(c.Key("github-clone-actions")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						if cs.errMsg != "" {
							ui.Text(c, cs.errMsg).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeDanger(c)).
								Grow(1).
								SingleLine()
						} else {
							ui.Spacer(c)
						}

						cloneLabel := "克隆"
						if cs.cloning {
							cloneLabel = "克隆中…"
						}
						canClone := !cs.cloning && strings.TrimSpace(cs.search) != "" && strings.TrimSpace(cs.parentDir) != ""
						Button(c.Key("github-clone-submit-btn"), ButtonOpts{
							Text:     cloneLabel,
							Disabled: !canClone,
							OnClick: func() {
								startGitHubClone(s, cs)
							},
							Variant: ButtonPrimary,
						})
					})
				modalResizeGrips(c, modalDragGitHubClone, width, height)
			})
	})
}

// renderGitHubRepoRow 渲染仓库列表中的一行：单击选中（写入输入框），双击直接克隆。
func renderGitHubRepoRow(c *ui.Context, s *chatState, cs *githubCloneState, fullName, title string) {
	selected := cloneNameFromInput(cs.search) == fullName
	row := ui.ButtonBase(c.Key("github-clone-repo-"+fullName)).
		Label(title).
		Padding(Space2, Space3).
		Radius(RadiusSM).
		FillWidth()
	if selected {
		row.Background(ThemeSelected(c).Alpha(0.4))
	} else if row.Hovered() {
		row.Background(ThemeHover(c))
	}
	if row.Clicked() {
		cs.search = fullName
		cs.errMsg = ""
		s.invalidate()
	}
	if row.DoubleClicked() {
		cs.search = fullName
		startGitHubClone(s, cs)
	}
	row.Children(func() {
		ui.Text(c, title).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemePrimaryText(c)).
			SingleLine()
	})
}
