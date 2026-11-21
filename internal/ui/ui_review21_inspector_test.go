package mcodeui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestUIReview21ConcurrentInspectorReadWrite(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	dir1 := filepath.Join(t.TempDir(), "ws1")
	dir2 := filepath.Join(t.TempDir(), "ws2")
	fake.SetThreadWorkDir(dir1)
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "line1\nline2\nline3\n", TotalSize: 18},
	})
	s := newChatState(fake)
	defer s.close()
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "p1", Path: dir1}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "t1"}}
	s.selectedThreadID = "t1"
	s.mu.Unlock()

	inspectorSelect(s, sidePaneFileID, false)
	getGitPaneState(s)

	tabSnap := fileManagerTabSnapshot{path: "a.txt", kind: "text", content: "line1\nline2\nline3\n", hlFirst: 1, hlLast: 2, reveal: true, revealSeq: 1}
	tt := ui.NewTester(func(c *ui.Context) {
		st := inspectorSession(s)
		if st == nil {
			return
		}
		inspectorSyncVisibility(st)
		ui.Column(c.Key("repro-root")).Fill().Children(func() {
			renderInspectorHeader(c, s, st, false)
			inspectorFollowCodePreview(c, s, tabSnap, "repro-")
		})
	}, 800, 500)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch i % 5 {
			case 0:
				inspectorSelect(s, sidePaneFileID, false)
			case 1:
				inspectorSelect(s, sidePanePreviewID, false)
			case 2:
				inspectorClose(s)
			case 3:
				getGitPaneState(s)
			case 4:
				if st := inspectorSession(s); st != nil {
					inspectorFollowReset(s, st)
				}
			}
		}
	})

	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				fake.SetThreadWorkDir(dir1)
			} else {
				fake.SetThreadWorkDir(dir2)
			}
			inspectorValidateWorkspace(s)
		}
	})

	for range 40 {
		tt.Frame()
	}
	close(stop)
	wg.Wait()
}

func TestUIReview21FollowNonTextClearsReveal(t *testing.T) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		path    string
		content FileContent
	}{
		{
			name:    "binary",
			path:    "bin.dat",
			content: FileContent{Path: "bin.dat", Kind: "binary", TotalSize: 16},
		},
		{
			name:    "empty",
			path:    "empty.txt",
			content: FileContent{Path: "empty.txt", Kind: "text", Text: "", TotalSize: 0},
		},
		{
			name:    "image",
			path:    "pic.png",
			content: FileContent{Path: "pic.png", Kind: "image", Data: pngBuf.Bytes(), TotalSize: int64(pngBuf.Len())},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, s, tt := inspectorFollowTestState(t)
			fake.SetFileContents(map[string]FileContent{tc.path: tc.content})

			inspectorFollowUpdate(s, "t1", "read-"+tc.name, "read", `{"path":"`+tc.path+`","start_line":5}`, "completed")
			waitFor(t, tt, func() bool {
				st := getFileManagerState(s)
				fileManagerMu.Lock()
				defer fileManagerMu.Unlock()
				tab := st.findTabLocked(tc.path)
				return tab != nil && !tab.fileLoading
			})
			tt.Frame()

			st := getFileManagerState(s)
			fileManagerMu.Lock()
			tab := st.findTabLocked(tc.path)
			reveal := tab != nil && tab.reveal
			fileManagerMu.Unlock()
			if reveal {
				t.Fatalf("%s 加载完成后 tab.reveal 仍为 true", tc.name)
			}

			s.mu.Lock()
			slot := inspectorExistingSessionLocked(s, "t1")
			ignoreScroll := slot != nil && slot.follow != nil && slot.follow.ignoreScroll
			s.mu.Unlock()
			if ignoreScroll {
				t.Fatalf("%s 加载完成后 ignoreScroll 仍为 true", tc.name)
			}
		})
	}
}

func TestUIReview21ArrowKeyResumesFollow(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	inspectorSelect(s, sidePaneDiffID, false)
	tt.Frame()

	inspectorFollowPause(s, "t1")
	inspectorFollowUpdate(s, "t1", "read-1", "read", `{"path":"sub/b.go","start_line":10}`, "completed")
	tt.Frame()
	if got := inspectorFollowBadge(s, "t1", sidePaneFileID); got != 1 {
		t.Fatalf("期望文件角标为 1，得到 %d", got)
	}

	clickText(t, tt, "改动")
	tt.Key(0, ui.KeyLeft)
	tt.Advance(0)

	inspectorFollowWaitFile(t, tt, s, "sub/b.go")
	if inspectorFollowPaused(s) {
		t.Fatal("方向键切到带角标标签后未恢复跟随")
	}
	if got := inspectorFollowBadge(s, "t1", sidePaneFileID); got != 0 {
		t.Fatalf("方向键恢复跟随后角标未清零: %d", got)
	}
	if st := inspectorSession(s); st.Active != sidePaneFileID {
		t.Fatalf("方向键切换后活动标签错误: %s", st.Active)
	}
}

func TestUIReview21NarrowHeaderCloseButtonBounds(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	inspectorSelect(s, sidePaneFileID, false)
	s.mu.Lock()
	st := inspectorExistingSessionLocked(s, "t1")
	f := inspectorFollowLocked(st)
	for _, id := range []string{sidePaneFileID, sidePaneDiffID, sidePaneGitID, sidePanePreviewID} {
		f.badges[id] = 12
	}
	s.mu.Unlock()

	const width = 260
	const height = 60
	tt := ui.NewTester(func(c *ui.Context) {
		renderInspectorHeader(c, s, st, false)
	}, width, height)
	tt.Frame()

	button, ok := tt.Find("关闭右栏")
	if !ok {
		t.Fatal("窄宽度下未找到关闭右栏按钮")
	}
	if button.W <= 0 || button.H <= 0 || button.X < 0 || button.Y < 0 || button.X+button.W > width || button.Y+button.H > height {
		t.Fatalf("关闭右栏按钮超出面板范围: bounds=%+v panel=%dx%d", button, width, height)
	}
}

type uiReview21DeadlineClient struct {
	Client
	mu             sync.Mutex
	filesDeadlines []bool
	diffDeadlines  []bool
}

func (c *uiReview21DeadlineClient) FilesRead(ctx context.Context, threadID, path string, offset, length int64) (FileContent, error) {
	_, ok := ctx.Deadline()
	c.mu.Lock()
	c.filesDeadlines = append(c.filesDeadlines, ok)
	c.mu.Unlock()
	return c.Client.FilesRead(ctx, threadID, path, offset, length)
}

func (c *uiReview21DeadlineClient) Diff(ctx context.Context, threadID, turnID string) (ThreadDiff, error) {
	_, ok := ctx.Deadline()
	c.mu.Lock()
	c.diffDeadlines = append(c.diffDeadlines, ok)
	c.mu.Unlock()
	return c.Client.Diff(ctx, threadID, turnID)
}

func TestUIReview21FilesReadContextHasDeadline(t *testing.T) {
	testenv.TempHome(t)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	fake := NewFakeClient()
	workDir := filepath.Join(t.TempDir(), "workspace")
	fake.SetThreadWorkDir(workDir)
	fake.SetFileContents(map[string]FileContent{
		"a.txt":   {Path: "a.txt", Kind: "text", Text: "hello\n", HasMore: true, NextOffset: 6, TotalSize: 12},
		"pic.png": {Path: "pic.png", Kind: "image", Data: pngBuf.Bytes(), TotalSize: int64(pngBuf.Len())},
	})
	client := &uiReview21DeadlineClient{Client: fake}
	s := newChatState(client)
	defer s.close()
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "p1", Path: workDir}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "t1"}}
	s.selectedThreadID = "t1"
	s.mu.Unlock()

	s.openFileManagerFile("t1", "a.txt", true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fileManagerTabText(s, "a.txt") != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.loadMoreFileManagerTab("a.txt")
	s.openFileManagerFile("t1", "pic.png", true)
	s.loadDiff("t1", "")
	s.ensureThreadChangeStats("t1", false)

	for time.Now().Before(deadline) {
		client.mu.Lock()
		nFiles := len(client.filesDeadlines)
		nDiff := len(client.diffDeadlines)
		client.mu.Unlock()
		if nFiles >= 4 && nDiff >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client.mu.Lock()
	filesDeadlines := append([]bool(nil), client.filesDeadlines...)
	diffDeadlines := append([]bool(nil), client.diffDeadlines...)
	client.mu.Unlock()

	if len(filesDeadlines) < 4 {
		t.Fatalf("FilesRead 调用次数不足: %d", len(filesDeadlines))
	}
	for i, hasDeadline := range filesDeadlines {
		if !hasDeadline {
			t.Fatalf("第 %d 次 FilesRead 的 ctx 缺少 Deadline", i)
		}
	}
	// 改动统计改走 DiffStats（只取三个数字），不再借 Diff 生成整会话补丁。
	if len(diffDeadlines) < 1 {
		t.Fatalf("Diff 调用次数不足: %d", len(diffDeadlines))
	}
	if len(fake.DiffStatsCalls()) == 0 {
		t.Fatal("DiffStats 未被调用")
	}
	for i, hasDeadline := range diffDeadlines {
		if !hasDeadline {
			t.Fatalf("第 %d 次 Diff 的 ctx 缺少 Deadline", i)
		}
	}
}
