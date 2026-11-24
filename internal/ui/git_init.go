package mcodeui

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
)

type gitInitClient interface {
	GitInit(context.Context, string) error
}

// canGitInit 判断后端客户端是否支持初始化 Git 仓库。
func canGitInit(s *chatState) bool {
	if s == nil || s.client == nil {
		return false
	}
	_, ok := s.client.(gitInitClient)
	return ok
}

// isGitInitBusy 返回指定会话是否正在执行 Git 初始化。
func isGitInitBusy(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	return hs.initBusy[threadID]
}

// startGitInit 在后台初始化指定会话工作区的 Git 仓库；
// 成功后依次刷新头部 Git 状态、Git 面板并打开右栏 Git 标签，失败时通过 toast 提示错误。
func startGitInit(s *chatState, threadID string) {
	if s == nil || s.client == nil || threadID == "" {
		return
	}
	client, ok := s.client.(gitInitClient)
	if !ok {
		return
	}
	if s.isThreadRunning(threadID) {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	if hs.initBusy[threadID] {
		chatHeaderMu.Unlock()
		return
	}
	hs.initBusy[threadID] = true
	hs.initConfirmOpen = false
	chatHeaderMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.GitInit(ctx, threadID)
		chatHeaderMu.Lock()
		delete(hs.initBusy, threadID)
		chatHeaderMu.Unlock()
		if err != nil {
			s.setToast("初始化 Git 失败: " + err.Error())
			s.invalidate()
			return
		}
		s.setToast("已初始化 Git 仓库")
		s.loadGitState(threadID)
		if ps := getGitPaneState(s); ps != nil {
			ps.requestRefresh()
		}
		inspectorSelect(s, sidePaneGitID, false)
		s.invalidate()
	}()
}

// renderGitInitActionButton 渲染「初始化」/「初始化 Git」操作按钮，进行中时使用不触发整窗重建的 Draw 指示器。
func renderGitInitActionButton(c *ui.Context, key, text string, variant ButtonVariant, size float32, icon *ui.SVG, busy, threadRunning bool, onClick func()) ui.Element {
	size = buttonSize(size)
	pad, radius, font, iconSize := Space3, RadiusMD, TypeBody, IconMD
	if size == ControlSM {
		pad, radius, font, iconSize = Space3, RadiusSM, TypeFootnote, IconSM
	}
	disabled := busy || threadRunning
	b := ui.ButtonBase(c.Key(key)).
		Height(size).
		Radius(radius).
		Gap(Space2).
		Padding(0, pad).
		Label(text)
	if threadRunning {
		b.Description("会话运行中，暂不可初始化")
	}
	if disabled {
		b.Disabled(true)
	} else if onClick != nil {
		b.OnClick(onClick)
	}
	fg := ThemePrimaryText(c)
	if variant == ButtonPrimary {
		fg = ThemeOnAccent(c)
	}
	hoverTarget, pressTarget := float32(0), float32(0)
	if !disabled && b.Hovered() {
		hoverTarget = 1
	}
	if !disabled && b.Pressed() {
		pressTarget = 1
	}
	hover := b.AnimateTo("button-hover", hoverTarget, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap})
	press := b.AnimateTo("button-press", pressTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseSnap})
	bg := buttonBackground(variant, false, windowActiveFor(c), hover, press, buttonColors{
		accent: ThemeAccent(c), accentHover: ThemeAccentHover(c), accentPressed: ThemeAccentPressed(c),
		tertiary: ThemeTertiaryText(c), overlaySelected: ThemeOverlaySelected(c),
		overlayHover: ThemeOverlayHover(c), overlayPressed: ThemeOverlayPressed(c), danger: ThemeDanger(c),
	})
	if disabled && variant != ButtonPrimary {
		fg = ThemeTertiaryText(c)
	}
	b.Background(bg)
	if variant == ButtonSecondary {
		b.Border(1, ThemeBorder(c))
	}
	focusRing(c, b)
	scale := motionPressScale(c, b, "button-press-scale", !disabled && b.Pressed())
	b.Scale(scale, scale).Children(func() {
		if busy {
			renderRunningGlyph(c, key+"-running", iconSize, fg)
		} else if icon != nil {
			ui.Icon(c, icon).Size(iconSize, iconSize).TextColor(fg)
		}
		ui.Text(c, text).FontSize(uiFont(font)).FontWeight(500).TextColor(fg).SingleLine()
	})
	return b
}
