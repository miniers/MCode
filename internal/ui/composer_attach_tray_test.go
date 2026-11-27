package mcodeui

import (
	"fmt"
	"image"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestAttachTrayEmbeddedInComposerCard(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("消息输入框")
		return ok
	})
	tt.Advance(DurSlow)
	tt.Frame()

	emptyCard, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatal("找不到消息输入框")
	}

	s.mu.Lock()
	s.attachments = []composerAttachment{{
		LocalID:  "emb-1",
		ID:       "att-1",
		Name:     "composer.go",
		MimeType: "text/plain",
		Size:     18 * 1024,
	}}
	s.mu.Unlock()
	s.invalidate()

	var chipRect, cardWithAtt ui.Rect
	waitFor(t, tt, func() bool {
		var okChip, okCard bool
		chipRect, okChip = tt.Find("附件 composer.go")
		cardWithAtt, okCard = tt.Find("消息输入框")
		return okChip && okCard && chipRect.H > 0
	})

	if chipRect.X < cardWithAtt.X || chipRect.X+chipRect.W > cardWithAtt.X+cardWithAtt.W {
		t.Fatalf("附件胶囊不在输入卡片横向范围内: chip=%+v card=%+v", chipRect, cardWithAtt)
	}
	if chipRect.Y < cardWithAtt.Y || chipRect.Y+chipRect.H > cardWithAtt.Y+cardWithAtt.H {
		t.Fatalf("附件胶囊不在输入卡片纵向范围内: chip=%+v card=%+v", chipRect, cardWithAtt)
	}
	if sendRect, ok := tt.Find("发送"); ok && chipRect.Y+chipRect.H > sendRect.Y {
		t.Fatalf("附件条应位于卡片顶部（底栏之上）: chip=%+v send=%+v", chipRect, sendRect)
	}

	// 清空后恢复空卡高度，不占多余空间。
	s.removeAttachment("emb-1")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("附件 composer.go")
		return !ok
	})
	tt.Advance(DurBase)
	tt.Frame()
	restoredCard, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatal("清空后找不到消息输入框")
	}
	if restoredCard.H != emptyCard.H {
		t.Fatalf("没有附件和提示时应恢复原卡片高度: got %.1f, want %.1f", restoredCard.H, emptyCard.H)
	}
}

func TestAttachTrayFileChipBadgeNameSizeAndCrossfade(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.attachments = []composerAttachment{
		{
			LocalID:  "chip-go",
			ID:       "att-go",
			Name:     "main.go",
			MimeType: "text/plain",
			Size:     18 * 1024,
		},
		{
			LocalID:  "chip-pdf",
			ID:       "att-pdf",
			Name:     "report.pdf",
			MimeType: "application/pdf",
			Size:     2 * 1024,
		},
		{
			LocalID:   "chip-err",
			Name:      "failed.go",
			MimeType:  "text/plain",
			Size:      4 * 1024,
			Err:       "网络超时",
			RetryData: []byte("package main"),
		},
	}
	s.mu.Unlock()

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		return tt.HasText("GO") && tt.HasText("main.go") && tt.HasText("18 KB") &&
			tt.HasText("PDF") && tt.HasText("report.pdf") && tt.HasText("2 KB") &&
			tt.HasText("failed.go") && tt.HasText("网络超时")
	})

	chipBefore, ok := tt.Find("附件 main.go")
	if !ok {
		t.Fatal("找不到附件 main.go")
	}
	errBefore, ok := tt.Find("附件 failed.go")
	if !ok {
		t.Fatal("找不到失败附件 failed.go")
	}

	// 悬停普通胶囊：× 与文件大小在同一位置交叉淡入，宽度不变。
	tt.Move(chipBefore.X+chipBefore.W/2, chipBefore.Y+chipBefore.H/2)
	tt.Advance(DurFast + 20*time.Millisecond)
	tt.Frame()
	chipAfter, ok := tt.Find("附件 main.go")
	if !ok {
		t.Fatal("悬停后找不到附件 main.go")
	}
	if chipAfter.W != chipBefore.W {
		t.Fatalf("悬停交叉淡入不应改变胶囊宽度: before=%.1f after=%.1f", chipBefore.W, chipAfter.W)
	}

	// 悬停失败胶囊：重试按钮左移让出 ×，宽度不变，且 × 可点击移除。
	retryBefore, ok := tt.Find("重试上传 failed.go")
	if !ok {
		t.Fatal("找不到重试上传 failed.go")
	}
	tt.Move(errBefore.X+errBefore.W/2, errBefore.Y+errBefore.H/2)
	tt.Advance(DurFast + 20*time.Millisecond)
	tt.Frame()
	errAfter, ok := tt.Find("附件 failed.go")
	if !ok {
		t.Fatal("悬停后找不到失败附件 failed.go")
	}
	if errAfter.W != errBefore.W {
		t.Fatalf("悬停失败胶囊不应改变宽度: before=%.1f after=%.1f", errBefore.W, errAfter.W)
	}
	retryAfter, ok := tt.Find("重试上传 failed.go")
	if !ok {
		t.Fatal("悬停后找不到重试上传 failed.go")
	}
	if retryAfter.X >= retryBefore.X {
		t.Fatalf("悬停失败胶囊时重试按钮应左移让出 ×: before.X=%.1f after.X=%.1f", retryBefore.X, retryAfter.X)
	}
	if err := tt.Click("删除附件 failed.go"); err != nil {
		t.Fatalf("点击悬停后的删除按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("failed.go")
	})
}

func TestAttachTrayOverflowMoreModalAndClear(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	// 1 个附件时不显示「清空」。
	s.mu.Lock()
	s.attachments = []composerAttachment{{
		LocalID:  "ov-01",
		ID:       "att-01",
		Name:     "file-01-alpha.go",
		MimeType: "text/plain",
		Size:     10 * 1024,
	}}
	s.mu.Unlock()

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText("file-01-alpha.go") })
	if tt.HasText("清空") {
		t.Fatal("只有 1 个附件时不应显示「清空」")
	}

	// 2 个附件时显示「清空」。
	s.mu.Lock()
	s.attachments = append(s.attachments, composerAttachment{
		LocalID:  "ov-02",
		ID:       "att-02",
		Name:     "file-02-beta.go",
		MimeType: "text/plain",
		Size:     10 * 1024,
	})
	s.mu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("清空") })

	// 10 个附件超出两行：显示「+N 个」，且 N 与可见附件数之和等于总数 10。
	s.mu.Lock()
	for i := 3; i <= 10; i++ {
		s.attachments = append(s.attachments, composerAttachment{
			LocalID:  fmt.Sprintf("ov-%02d", i),
			ID:       fmt.Sprintf("att-%02d", i),
			Name:     fmt.Sprintf("module-handler-%02d.go", i),
			MimeType: "text/plain",
			Size:     10 * 1024,
		})
	}
	s.mu.Unlock()
	s.invalidate()

	moreRe := regexp.MustCompile(`^\+(\d+) 个$`)
	var moreText string
	var hiddenN int
	waitFor(t, tt, func() bool {
		for _, txt := range tt.Texts() {
			if m := moreRe.FindStringSubmatch(txt); len(m) == 2 {
				n, err := strconv.Atoi(m[1])
				if err == nil && n > 0 {
					moreText = txt
					hiddenN = n
					return true
				}
			}
		}
		return false
	})
	tt.Frame()

	s.mu.Lock()
	allAtts := append([]composerAttachment(nil), s.attachments...)
	s.mu.Unlock()
	visibleCount := 0
	for _, a := range allAtts {
		if tt.HasText(a.Name) {
			visibleCount++
		}
	}
	if visibleCount+hiddenN != len(allAtts) {
		t.Fatalf("可见数 %d + 折叠数 %d != 总附件数 %d (%q)", visibleCount, hiddenN, len(allAtts), moreText)
	}

	// 点开「+N 个」弹出管理弹窗（全部附件），可逐个移除或清空全部。
	if err := tt.Click(moreText); err != nil {
		t.Fatalf("点击 %q 失败: %v", moreText, err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("全部附件（10）") && tt.HasText("module-handler-10.go")
	})

	if err := tt.Click("移除 file-01-alpha.go"); err != nil {
		t.Fatalf("管理弹窗移除单个附件失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("全部附件（9）") && !tt.HasText("file-01-alpha.go")
	})

	if err := tt.Click("清空全部附件"); err != nil {
		t.Fatalf("管理弹窗清空全部失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		n := len(s.attachments)
		s.mu.Unlock()
		return n == 0 && !tt.HasText("全部附件（")
	})

	// 再加 2 个附件验证外露「清空」按钮可用。
	s.mu.Lock()
	s.attachments = []composerAttachment{
		{LocalID: "c1", ID: "a1", Name: "one.go", MimeType: "text/plain", Size: 1024},
		{LocalID: "c2", ID: "a2", Name: "two.go", MimeType: "text/plain", Size: 1024},
	}
	s.mu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("清空") })
	if err := tt.Click("清空附件"); err != nil {
		t.Fatalf("点击清空按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		n := len(s.attachments)
		s.mu.Unlock()
		return n == 0 && !tt.HasText("one.go")
	})
}

func TestAttachTrayNoticePersistsUntilClosed(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	const notice = "已忽略 big.zip：超过单个 20MB 上限"
	s.setAttachmentNotice(notice)

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText(notice) })

	// 超过原 4 秒自动清理时长后仍保持可见。
	tt.Advance(10 * time.Second)
	tt.Frame()
	if !tt.HasText(notice) {
		t.Fatal("附件提示条不应定时自动清除")
	}

	if err := tt.Click("关闭附件提示"); err != nil {
		t.Fatalf("点击关闭提示按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText(notice) && s.attachmentNotice() == ""
	})
}

func TestAttachTraySendBlockHint(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.attachments = []composerAttachment{{
		LocalID:   "blk-1",
		Name:      "shot.png",
		MimeType:  "image/png",
		Uploading: true,
	}}
	s.mu.Unlock()

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText("附件上传中") })

	// 失败态优先显示失败原因。
	s.mu.Lock()
	s.attachments[0].Uploading = false
	s.attachments[0].Err = "上传超时"
	s.mu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool { return tt.HasText("有附件上传失败") })

	// 上传完成后阻塞提示消失。
	s.mu.Lock()
	s.attachments[0].Err = ""
	s.attachments[0].ID = "att-ok"
	s.mu.Unlock()
	s.invalidate()
	waitFor(t, tt, func() bool {
		return !tt.HasText("有附件上传失败") && !tt.HasText("附件上传中")
	})
}

func TestAttachTrayUploadSpinnerStopsAfterFinish(t *testing.T) {
	testenv.TempHome(t)
	setAppReduceMotion(t, false)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.mu.Lock()
	s.attachments = []composerAttachment{
		{
			LocalID:   "spin-img",
			Name:      "shot.png",
			MimeType:  "image/png",
			Uploading: true,
			Thumb:     attachmentThumbnail("image/png", testPNGBytes(t)),
		},
		{
			LocalID:   "spin-file",
			Name:      "main.go",
			MimeType:  "text/plain",
			Size:      1024,
			Uploading: true,
		},
	}
	s.mu.Unlock()

	tt := composerTester(s)
	waitFor(t, tt, func() bool { return tt.HasText("main.go") })
	imgRect, okImg := tt.Find("放大预览 shot.png")
	chipRect, okChip := tt.Find("附件 main.go")
	if !okImg || !okChip {
		t.Fatalf("找不到上传中的附件区域: img=%v chip=%v", okImg, okChip)
	}
	toPixelRect := func(r ui.Rect) image.Rectangle {
		return image.Rect(int(r.X), int(r.Y), int(r.X+r.W)+1, int(r.Y+r.H)+1)
	}
	before := cloneImage(tt.Image())
	tt.Advance(100 * time.Millisecond)
	if !bandDiffers(before, tt.Image(), toPixelRect(imgRect)) {
		t.Fatal("图片上传中转圈应持续驱动重绘")
	}
	if !bandDiffers(before, tt.Image(), toPixelRect(chipRect)) {
		t.Fatal("文件上传中转圈应持续驱动重绘")
	}

	// 上传结束：卸载转圈，推进时钟后不应继续请求下一帧，且附件区域画面不再变化。
	s.mu.Lock()
	for i := range s.attachments {
		s.attachments[i].Uploading = false
		s.attachments[i].ID = fmt.Sprintf("att-%d", i+1)
	}
	s.mu.Unlock()
	s.invalidate()
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()

	if tt.FramePending() {
		t.Fatal("上传结束后推进时钟不应再请求下一帧")
	}
	settled := cloneImage(tt.Image())
	tt.Advance(200 * time.Millisecond)
	if tt.FramePending() {
		t.Fatal("上传结束静止后仍请求下一帧")
	}
	if bandDiffers(settled, tt.Image(), toPixelRect(imgRect)) {
		t.Fatal("上传结束后图片附件区域不应继续重绘")
	}
	if bandDiffers(settled, tt.Image(), toPixelRect(chipRect)) {
		t.Fatal("上传结束后文件附件区域不应继续重绘")
	}
}
