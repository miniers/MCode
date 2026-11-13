package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/fsutil"
	_ "modernc.org/sqlite"
)

// frontdeskHandoffSection 为普通会话提供移交提示。
func frontdeskHandoffSection(dataDir, threadID string) string {
	if strings.TrimSpace(threadID) == "" {
		return ""
	}
	cfg, err := config.Load(dataDir)
	if err != nil || !cfg.Orchestration.Steward.Enabled {
		return ""
	}
	dbPath, err := filepath.Abs(filepath.Join(dataDir, "mcode.db"))
	if err != nil {
		return ""
	}
	dbURL := fsutil.FileURL(dbPath)
	dbURL.RawQuery = "mode=ro"
	if db, err := sql.Open("sqlite", dbURL.String()); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var id string
		readErr := db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", "steward.feed_thread_id").Scan(&id)
		cancel()
		_ = db.Close()
		if readErr == nil && id == threadID {
			return ""
		}
	}
	return fmt.Sprintf(`## 可以交给总管
需要跨会话长期跟进、要设成定时任务、或要同时派给多个子代理的事，建议用户按 %s 或在命令面板里选「打开%s」，在主会话里交给「%s」。
移交时把已知现象、相关文件和验收要求说清楚。`, frontdeskHandoffShortcut(runtime.GOOS), cfg.Orchestration.Steward.Label(), cfg.Orchestration.Steward.Label())
}

func frontdeskHandoffShortcut(goos string) string {
	if goos == "windows" {
		return "Ctrl+Shift+T"
	}
	return "⇧⌘T"
}
