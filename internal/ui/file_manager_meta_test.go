package mcodeui

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestFileManagerMetaSegments(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	bmp := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1280, 720)))

	cases := []struct {
		name string
		tab  fileManagerTabSnapshot
		want []string
	}{
		{
			name: "text_complete",
			tab: fileManagerTabSnapshot{
				kind:      "text",
				totalSize: 1536,
				modTime:   now.Add(-5 * time.Minute),
				lineCount: 42,
			},
			want: []string{formatFileSize(1536), formatRelativeTimeAt(now.Add(-5*time.Minute), now), "42 行"},
		},
		{
			name: "image_dimensions",
			tab: fileManagerTabSnapshot{
				kind:      "image",
				totalSize: 2048,
				modTime:   now.Add(-2 * time.Hour),
				lineCount: 10,
				imageBmp:  bmp,
			},
			want: []string{formatFileSize(2048), formatRelativeTimeAt(now.Add(-2*time.Hour), now), "1280×720"},
		},
		{
			name: "text_incomplete_has_more",
			tab: fileManagerTabSnapshot{
				kind:      "text",
				totalSize: 1024,
				modTime:   now.Add(-1 * time.Minute),
				hasMore:   true,
				lineCount: 20,
			},
			want: []string{formatFileSize(1024), formatRelativeTimeAt(now.Add(-1*time.Minute), now)},
		},
		{
			name: "zero_mod_time",
			tab: fileManagerTabSnapshot{
				kind:      "text",
				totalSize: 128,
				lineCount: 3,
			},
			want: []string{formatFileSize(128), "3 行"},
		},
		{
			name: "loading_hidden",
			tab: fileManagerTabSnapshot{
				kind:        "text",
				totalSize:   128,
				lineCount:   3,
				fileLoading: true,
			},
			want: nil,
		},
		{
			name: "error_hidden",
			tab: fileManagerTabSnapshot{
				kind:      "text",
				totalSize: 128,
				lineCount: 3,
				fileErr:   "read error",
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fileManagerMetaSegmentsAt(tc.tab, now)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("fileManagerMetaSegmentsAt() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFileManagerMetaRow(t *testing.T) {
	const content = "line1\nline2\nline3"
	modTime := time.Now().Add(-5 * time.Minute)

	fake := treeTestBase()
	fake.SetFileTree(map[string][]FileEntry{
		"": {
			{Name: "three.txt", Path: "three.txt", Size: int64(len(content)), ModTime: modTime},
		},
	})
	fake.SetFileContents(map[string]FileContent{
		"three.txt": {
			Path:      "three.txt",
			Kind:      "text",
			Text:      content,
			TotalSize: int64(len(content)),
		},
	})

	s, tt := openFileTreeForTest(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("three.txt") })

	openTestFileManagerFile(t, s, "three.txt", true)
	waitForFileManagerText(t, tt, s, "three.txt", content)

	wantSize := formatFileSize(int64(len(content)))
	wantTime := formatRelativeTime(modTime)
	waitFor(t, tt, func() bool {
		return tt.HasText("3 行") && tt.HasText(wantSize) && tt.HasText(wantTime)
	})
}
