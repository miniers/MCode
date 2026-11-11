package mcodeui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
)

type appUpdateNotesClient interface {
	UpdateNotes() string
}

type appVersionClient interface {
	AppVersion() string
}

func shouldShowWhatsNew(current, lastSeen, section string) (show bool, persist bool) {
	if current == "" || current == "dev" || current == lastSeen {
		return false, false
	}
	return lastSeen != "" && strings.TrimSpace(section) != "", true
}

func updateButtonText(installable bool) string {
	if installable {
		return "更新并重启"
	}
	return "下载"
}

var (
	whatsNewPersistMu sync.Mutex
	// whatsNewPersisted 记录每个数据目录已触发写盘的最新版本，避免渲染路径每帧重复起 goroutine。
	whatsNewPersisted = make(map[string]string)
)

// persistLastSeenVersion 在后台写入 LastSeenVersion。同一个版本只触发一次，
// 写盘不阻塞渲染线程；即使写盘失败也不在下一帧重试，避免渲染循环反复读盘。
func persistLastSeenVersion(st *SettingsState, version string) {
	if st == nil || version == "" {
		return
	}
	whatsNewPersistMu.Lock()
	if whatsNewPersisted[st.dataDir] == version {
		whatsNewPersistMu.Unlock()
		return
	}
	whatsNewPersisted[st.dataDir] = version
	whatsNewPersistMu.Unlock()
	go st.updateAndSave(func(cfg *config.Config) { cfg.LastSeenVersion = version })
}

func initWhatsNew(s *chatState, st *SettingsState, fs *sidebarFooterState) {
	if fs.whatsNewChecked {
		return
	}
	versionClient, ok := s.client.(appVersionClient)
	if !ok {
		return
	}
	fs.whatsNewChecked = true
	version := versionClient.AppVersion()
	st.cfgMu.Lock()
	lastSeen := st.cfg.LastSeenVersion
	st.cfgMu.Unlock()
	section := changelogSection(version)
	show, persist := shouldShowWhatsNew(version, lastSeen, section)
	if persist {
		persistLastSeenVersion(st, version)
	}
	if show {
		fs.whatsNewVersion = version
		fs.whatsNewSection = section
		fs.whatsNewOpen = true
	}
}

func renderUpdateCapsule(c *ui.Context, s *chatState, st *SettingsState, fs *sidebarFooterState, status AppUpdateStatus) {
	btn := ui.ButtonBase(c.Key("sidebar-update-capsule")).
		Label("有新版本 "+status.Latest).
		Radius(RadiusSM).
		Padding(Space1, Space2).
		Background(ThemeAccentSubtle(c))
	if btn.Clicked() {
		fs.updateOpen = !fs.updateOpen
		fs.whatsNewOpen = false
		s.invalidate()
	}
	progress := float32(1)
	if !reduceMotion(c) {
		if !fs.updateVisible {
			btn.Jump("update-capsule-in", 0)
		}
		progress = btn.AnimateTo("update-capsule-in", 1, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})
	}
	btn.Scale(0.95+0.05*progress, 0.95+0.05*progress).Translate(0, (1-progress)*motionRiseOffset).Opacity(progress).Children(func() {
		ui.Text(c, status.Latest).FontSize(uiFont(TypeCaption)).TextColor(ThemeAccent(c))
	})
	appPopoverAbove(c.Key("sidebar-update-popover"), btn, &fs.updateOpen, nil, func() {
		notes := ""
		if client, ok := s.client.(appUpdateNotesClient); ok {
			notes = client.UpdateNotes()
		}
		renderUpdatePopover(c, s, st, "MCode "+status.Latest, notes, &status)
	})
}

func renderUpdatePopover(c *ui.Context, s *chatState, st *SettingsState, title, notes string, status *AppUpdateStatus) {
	popoverCard(c, ui.Column(c.Key("update-popover-card"))).
		Width(360).
		MaxWidth(360).
		Padding(Space4).
		Gap(Space3).
		Children(func() {
			ui.Text(c, title).FontSize(uiFont(TypeBody)).FontWeight(600).TextColor(ThemePrimaryText(c))
			if strings.TrimSpace(notes) != "" {
				ui.Scroll(c.Key("update-popover-notes")).FillWidth().MaxHeight(320).Children(func() {
					markdown.View(c.Key("update-popover-markdown"), notes, swarmReportMarkdownOptions(c, s, TypeBody))
				})
			}
			if status == nil {
				return
			}
			globalAboutState.mu.Lock()
			installing, progress := globalAboutState.installing, globalAboutState.progress
			globalAboutState.mu.Unlock()
			text := updateButtonText(status.Installable)
			if installing {
				text = fmt.Sprintf("%.0f%%", progress*100)
			}
			Button(c, ButtonOpts{
				Key: "update-popover-action", Text: text, Variant: ButtonPrimary, Size: ControlSM, Disabled: installing,
				OnClick: func() {
					if status.Installable {
						if installer, ok := s.client.(appUpdateInstallClient); ok {
							startAppUpdateInstall(st, installer)
						}
					} else {
						openExternalURL(status.URL)
					}
				},
			})
		})
}
