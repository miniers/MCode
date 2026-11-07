package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// UpdateFeedURL 是 GitHub releases/latest 接口地址，可通过 -ldflags -X 覆盖；为空时不检查。
var UpdateFeedURL = "https://api.github.com/repos/miniers/MCode/releases/latest"

// AppVersion 当前应用版本，可在构建时通过 -ldflags -X 覆盖。
var AppVersion = "dev"

// UpdateInfo 描述一次更新检查的结果。
type UpdateInfo struct {
	Latest      string
	URL         string
	Available   bool
	Installable bool
}

type installableUpdate interface {
	Install(context.Context, func(downloaded, total int64)) error
}

var nativeUpdatesEnabled = mygo.Updater.Enabled
var nativeUpdateCheck = func(ctx context.Context) (string, installableUpdate, error) {
	update, err := mygo.Updater.Check(ctx)
	if err != nil || update == nil {
		return "", nil, err
	}
	return update.Version, update, nil
}
var relaunchAfterUpdate = mygo.App.Relaunch

// CompareSemver 比较两个语义化版本：a<b 返回 -1，相等 0，a>b 返回 1。
// 忽略 v 前缀；带 -beta、-nightly 等预发布后缀的版本低于同号正式版。
func CompareSemver(a, b string) int {
	an, apre := splitSemver(a)
	bn, bpre := splitSemver(b)
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

func splitSemver(v string) (nums [3]int, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	for i, part := range strings.SplitN(v, ".", 3) {
		nums[i], _ = strconv.Atoi(part)
	}
	return nums, pre
}

// UpdateChecker 定时检查新版本；HTTP 客户端、feed 地址与时钟可注入。
type UpdateChecker struct {
	Client         *http.Client
	FeedURL        string
	CurrentVersion string
	Now            func() time.Time
	Enabled        func() bool
	OnChecked      func()

	mu        sync.Mutex
	latest    UpdateInfo
	checkedAt time.Time
	pending   installableUpdate
}

// Check 拉取 releases/latest，跳过 prerelease，并与当前版本比较。
func (u *UpdateChecker) Check(ctx context.Context) (UpdateInfo, error) {
	if nativeUpdatesEnabled() {
		version, update, err := nativeUpdateCheck(ctx)
		if err != nil {
			return UpdateInfo{}, err
		}
		info := UpdateInfo{}
		if update != nil {
			version = strings.TrimPrefix(version, "v")
			info = UpdateInfo{Latest: version, URL: "https://github.com/miniers/MCode/releases/tag/v" + version, Available: true, Installable: true}
		}
		now := time.Now
		if u.Now != nil {
			now = u.Now
		}
		u.mu.Lock()
		u.pending = update
		u.latest = info
		u.checkedAt = now()
		u.mu.Unlock()
		return info, nil
	}
	if u.FeedURL == "" {
		return UpdateInfo{}, fmt.Errorf("update feed URL not configured")
	}
	client := u.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.FeedURL, nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return UpdateInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return UpdateInfo{}, fmt.Errorf("update feed: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return UpdateInfo{}, fmt.Errorf("update feed: decode: %w", err)
	}

	now := time.Now
	if u.Now != nil {
		now = u.Now
	}
	info := UpdateInfo{}
	if !rel.Prerelease && rel.TagName != "" {
		info = UpdateInfo{
			Latest:    strings.TrimPrefix(rel.TagName, "v"),
			URL:       rel.HTMLURL,
			Available: CompareSemver(rel.TagName, u.CurrentVersion) > 0,
		}
	}
	u.mu.Lock()
	u.latest = info
	u.pending = nil
	u.checkedAt = now()
	u.mu.Unlock()
	return info, nil
}

// Install 安装待处理的更新并重启应用。
func (u *UpdateChecker) Install(ctx context.Context, progress func(float64)) error {
	u.mu.Lock()
	update := u.pending
	u.mu.Unlock()
	if update == nil {
		return fmt.Errorf("没有可安装的更新")
	}
	err := update.Install(ctx, func(downloaded, total int64) {
		if progress == nil {
			return
		}
		value := float64(0)
		if total > 0 {
			value = max(0, min(1, float64(downloaded)/float64(total)))
		}
		progress(value)
	})
	if err != nil {
		return err
	}
	relaunchAfterUpdate()
	return nil
}

// Latest 返回最近一次检查结果。
func (u *UpdateChecker) Latest() UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.latest
}

func (u *UpdateChecker) checkIfEnabled(ctx context.Context) {
	if u.Enabled != nil && !u.Enabled() {
		return
	}
	info, err := u.Check(ctx)
	if err != nil {
		return
	}
	if info.Available {
		log.Printf("update available: %s (%s)", info.Latest, info.URL)
	}
	if u.OnChecked != nil {
		u.OnChecked()
	}
}

// Start 在后台定时检查：首次延迟 1 分钟，之后每 interval 一次，ctx 取消后退出。
// 未配置更新源且原生更新不可用时直接返回。
func (u *UpdateChecker) Start(ctx context.Context, interval time.Duration) {
	if u.FeedURL == "" && !nativeUpdatesEnabled() {
		return
	}
	go func() {
		delay := time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			u.checkIfEnabled(ctx)
			delay = interval
		}
	}()
}
