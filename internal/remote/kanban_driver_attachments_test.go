package remote

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestStartSessionAttachments(t *testing.T) {
	for _, mode := range []string{"新附件", "源消息", "重复开工", "并发开工", "源文件丢失", "源消息丢失"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "attachments.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			hub := NewStreamHub(StreamHubOptions{})
			defer hub.Close()
			b := NewMemoryBackend(hub)
			defer b.Close()
			b.SetDataDir(t.TempDir())
			b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
			if err := b.AttachStore(ctx, db); err != nil {
				t.Fatal(err)
			}
			b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})
			attachment, err := b.AttachmentsUpload(ctx, wire.AttachmentsUploadParams{
				Name: "截图.png", Data: base64.StdEncoding.EncodeToString(makeTestPNG(2, 2)),
			})
			if err != nil {
				t.Fatal(err)
			}
			req := kanban.SessionRequest{ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "读取截图", Attachments: []string{attachment.AttachmentID}}
			var sourcePath string
			if mode != "新附件" {
				source, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
				if err != nil {
					t.Fatal(err)
				}
				message, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: source.ThreadID, Text: "用户原消息", Attachments: req.Attachments})
				if err != nil {
					t.Fatal(err)
				}
				waitThreadIdle(t, b, ctx, source.ThreadID)
				req.SourceMessageID = *message.MessageID
				if _, ok := b.attachments.get(attachment.AttachmentID); ok {
					t.Fatal("源附件发送后应已从临时索引删除")
				}
				sourcePath = resolveMediaFilePath(filepath.Join(b.dataDir, "media", source.ThreadID), attachment.AttachmentID, "截图.png", "image/png")
				if mode == "源文件丢失" {
					if err := os.Remove(sourcePath); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "源消息丢失" {
					req.SourceMessageID = "missing"
				}
			}
			runs := 1
			if mode == "重复开工" {
				runs = 2
			}
			check := func(threadID string) {
				waitThreadIdle(t, b, ctx, threadID)
				messages, _, err := db.ListMessages(ctx, threadID, 0, 100)
				if err != nil || len(messages) == 0 || messages[0].Role != "user" || messages[0].Text != req.Prompt {
					t.Fatalf("首条开工消息不符: %+v, %v", messages, err)
				}
				if mode == "源文件丢失" || mode == "源消息丢失" {
					if len(messages[0].Media) != 0 {
						t.Fatalf("附件不可用时应继续发送文字: %+v", messages[0])
					}
					return
				}
				if len(messages[0].Media) != 1 || messages[0].Media[0].Name != "截图.png" {
					t.Fatalf("第一条消息缺少附件: %+v", messages[0])
				}
				media := messages[0].Media[0]
				if mode == "新附件" && media.ID != attachment.AttachmentID {
					t.Fatalf("新附件 ID 应原样传入: %+v", media)
				}
				if mode != "新附件" {
					if media.ID == attachment.AttachmentID {
						t.Fatal("已消费的源附件应重新上传")
					}
					if _, err := os.Stat(sourcePath); err != nil {
						t.Fatalf("新会话发送不得删除源文件: %v", err)
					}
				}
				if _, err := b.ThreadsMediaRead(ctx, wire.ThreadsMediaReadParams{ThreadID: threadID, MessageID: messages[0].ID, MediaID: media.ID, Variant: wire.MediaVariantFull}); err != nil {
					t.Fatalf("新会话应能读取截图: %v", err)
				}
			}
			if mode == "并发开工" {
				type result struct {
					threadID string
					err      error
				}
				results := make(chan result, 2)
				for i := 0; i < 2; i++ {
					go func() {
						threadID, err := b.StartSession(ctx, req)
						results <- result{threadID, err}
					}()
				}
				for i := 0; i < 2; i++ {
					res := <-results
					if res.err != nil {
						t.Fatal(res.err)
					}
					check(res.threadID)
				}
			} else {
				for i := 0; i < runs; i++ {
					threadID, err := b.StartSession(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					check(threadID)
				}
			}
		})
	}
}
