package mcodeui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type fileSearchMigrationRequest struct {
	threadID string
	query    string
}

type fileSearchMigrationClient struct {
	*FakeClient
	requests chan fileSearchMigrationRequest
}

func (f *fileSearchMigrationClient) SearchFiles(_ context.Context, threadID, query string) ([]FileMatch, error) {
	f.requests <- fileSearchMigrationRequest{threadID: threadID, query: query}
	return []FileMatch{{Name: "match.go", Path: "match.go"}}, nil
}

// 内嵌 FakeClient 会提升其 SearchFileEntries，这里必须同时覆盖，确保请求经过本替身。
func (f *fileSearchMigrationClient) SearchFileEntries(ctx context.Context, threadID, query string) (FileSearchResult, error) {
	entries, err := f.SearchFiles(ctx, threadID, query)
	return FileSearchResult{Entries: entries}, err
}

func TestFileManagerSearchMigrationCurrentThreadAndClear(t *testing.T) {
	client := &fileSearchMigrationClient{FakeClient: NewFakeClient(), requests: make(chan fileSearchMigrationRequest, 10)}
	s := newChatState(client)
	s.selectedThreadID = "current"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "current", ProjectID: "p1"}, {ID: "other", ProjectID: "p1"}}
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	st.threadID = "current"
	st.open = true
	st.showList = true
	st.entries = []FileEntry{{Name: "old.txt", Path: "old.txt"}}
	fileManagerMu.Unlock()
	t.Cleanup(func() {
		fileManagerMu.Lock()
		delete(fileManagerStates, s)
		fileManagerMu.Unlock()
	})

	tester := ui.NewTester(func(c *ui.Context) { renderFileManagerPanel(c, s) }, 800, 600)
	if err := tester.Click("搜索文件"); err != nil {
		t.Fatal(err)
	}
	tester.Type("match")
	select {
	case req := <-client.requests:
		if req.threadID != "current" || req.query != "match" {
			t.Fatalf("搜索请求 = %+v", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("搜索请求未发出")
	}
	waitFor(t, tester, func() bool { return tester.HasText("match.go") })
	if err := tester.Click("清除搜索"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText("old.txt") || tester.HasText("match.go") {
		t.Fatal("清除搜索后未返回目录视图")
	}
	fileManagerMu.Lock()
	query, searching := st.query, st.searching
	fileManagerMu.Unlock()
	if query != "" || searching {
		t.Fatalf("清除后搜索词 = %q，搜索中 = %v", query, searching)
	}
}
