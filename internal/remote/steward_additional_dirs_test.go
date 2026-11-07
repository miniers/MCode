package remote

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestStewardAdditionalDirs 覆盖附加目录的判定：总管主会话与话题会话带上全部已注册项目目录，
// 排除工作目录自身；普通会话为空。
func TestStewardAdditionalDirs(t *testing.T) {
	ctx := context.Background()
	backend := newStewardTestBackend(t, stewardTestThreadID)
	setTopicThreads(t, backend, topicTestThreadID)

	projectA, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath(projectA): %v", err)
	}
	projectB, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath(projectB): %v", err)
	}
	projectA.Path = filepath.Clean(projectA.Path)
	projectB.Path = filepath.Clean(projectB.Path)

	// 总管主会话的工作目录就是项目 A 时，只剩项目 B 作为附加目录。
	if got, want := backend.stewardAdditionalDirs(ctx, stewardTestThreadID, projectA.Path), []string{projectB.Path}; !reflect.DeepEqual(got, want) {
		t.Errorf("总管主会话: stewardAdditionalDirs = %v, want %v", got, want)
	}

	// 普通会话没有任何附加目录。
	if got := backend.stewardAdditionalDirs(ctx, regularTestThreadID, projectA.Path); got != nil {
		t.Errorf("普通会话: stewardAdditionalDirs = %v, want nil", got)
	}

	// 话题会话同样带上项目目录，工作目录不在项目表里时两个项目都出现且已排序。
	wantTopic := []string{projectA.Path, projectB.Path}
	sort.Strings(wantTopic)
	if got := backend.stewardAdditionalDirs(ctx, topicTestThreadID, filepath.Join(t.TempDir(), "steward")); !reflect.DeepEqual(got, wantTopic) {
		t.Errorf("话题会话: stewardAdditionalDirs = %v, want %v", got, wantTopic)
	}
}
