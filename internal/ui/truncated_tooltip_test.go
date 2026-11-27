package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTruncatedTooltip(t *testing.T) {
	const (
		fileName    = "very_long_component_filename_that_must_truncate_in_narrow_sidebar.tsx"
		filePath    = "src/components/" + fileName
		planHeading = "Implement dynamic truncated tooltip handling for plan approval title"
		procObject  = "internal/ui/very/long/target/path/that/exceeds/narrow/command/row.go"
	)

	cases := []struct {
		name        string
		accessLabel string
		wantTip     string
		narrowW     float32
		wideW       float32
		render      func(c *ui.Context, s *chatState)
	}{
		{
			name:        "file_manager_row",
			accessLabel: fileName,
			wantTip:     filePath,
			narrowW:     120,
			wideW:       800,
			render: func(c *ui.Context, s *chatState) {
				renderFileManagerRow(c, s, fileManagerRowProps{
					key:   "trunc-row",
					icon:  IconFile(),
					name:  fileName,
					entry: FileEntry{Name: fileName, Path: filePath},
				})
			},
		},
		{
			name:        "plan_card_title",
			accessLabel: planHeading,
			wantTip:     planHeading,
			narrowW:     240,
			wideW:       900,
			render: func(c *ui.Context, s *chatState) {
				renderPlanCard(c, s, Approval{
					RequestID: "req-plan-trunc",
					Detail:    "# " + planHeading,
				}, false)
			},
		},
		{
			name:        "command_process_object",
			accessLabel: procObject,
			wantTip:     procObject,
			narrowW:     160,
			wideW:       900,
			render: func(c *ui.Context, s *chatState) {
				renderCommandProcessRow(c, "cmd-trunc", 0, commandProcessView{
					name:   "go",
					object: procObject,
					cpu:    25,
				}, false)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runWidth := func(width float32, wantTip string) {
				t.Helper()
				s := &chatState{}
				tester := ui.NewTester(func(c *ui.Context) {
					ui.Box(c).Width(width).Children(func() {
						tc.render(c, s)
					})
				}, int(width)+80, 120)
				tester.Frame()

				info, ok := tester.Access(tc.accessLabel)
				if !ok {
					t.Fatalf("width=%g: access node %q not found: %v", width, tc.accessLabel, tester.AccessAll())
				}
				if info.Description != wantTip {
					t.Fatalf("width=%g: Tooltip/Description = %q, want %q", width, info.Description, wantTip)
				}
			}

			runWidth(tc.narrowW, tc.wantTip)
			runWidth(tc.wideW, "")
		})
	}
}
