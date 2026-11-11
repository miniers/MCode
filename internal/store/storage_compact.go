package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// 数据库文件回收（增量 vacuum）：
// SQLite 默认 auto_vacuum=0，删除数据只把页挂进 freelist，主文件不会变小；
// 切到 INCREMENTAL 后可以在删消息之后分批把空闲页还给文件系统。
//
// 设计要点：
//   - 切换 auto_vacuum 必须整库 VACUUM 一次，所以只在启动时做，且老库只做一次。
//   - VACUUM 会把整库重写进 WAL，必须立刻 checkpoint(TRUNCATE)，否则文件暂时更大。
//   - 回收按批进行并让出连接：全进程 MaxOpenConns(1)，一次清完会卡住整个界面。

// autoVacuumIncremental 是 SQLite 的增量 auto_vacuum 模式值。
const autoVacuumIncremental = 2

const (
	// 单批回收页数上限；1 页默认 4KB，2048 页约 8MB。
	defaultReclaimBatchPages = 2048
	// 批间让出连接的等待时长。
	defaultReclaimPause = 50 * time.Millisecond
)

// 预整理用的文件名后缀与进度轮询间隔：
//   - .compact-tmp 是 VACUUM INTO 的输出，校验通过后替换原库；
//   - .pre-compact 是替换过程中原库的临时备份。
const (
	compactTempSuffix       = ".compact-tmp"
	compactBackupSuffix     = ".pre-compact"
	compactMarkerSuffix     = ".compacted"
	compactProgressInterval = 200 * time.Millisecond
)

var (
	compactRewriteMinBytes int64 = 200 << 20
	compactRewriteInterval       = 30 * 24 * time.Hour
)

// CompactForIncrementalVacuum 在 store.Open 之前，用 VACUUM INTO 把整库重写成
// 已切到增量 auto_vacuum 的新文件，校验后替换原库。
// 已是增量模式的库仅在大小达到阈值且上次整理已满 30 天或没有整理记录时重写。
//
// 与在已打开的库上原地 VACUUM 相比，这条路径能给出真实进度：VACUUM INTO 边写边落盘，
// 轮询临时文件大小即可估算 done，启动界面据此显示进度条；同时新库先落在旁边，
// 校验失败时原库完全不受影响。progress 可为 nil，成功时最后一次调用 done == total。
//
// 返回 converted 表示本次是否真的替换了原库。库不存在、未达到重写条件，或 VACUUM INTO
// 没能把 auto_vacuum 写进新库时返回 false，交给 EnsureIncrementalAutoVacuum 原地兜底。
func CompactForIncrementalVacuum(ctx context.Context, path string, progress func(done, total int64)) (converted bool, err error) {
	started := time.Now()
	cleanPath := strings.TrimPrefix(path, "file:")
	info, err := os.Stat(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat db %s: %w", cleanPath, err)
	}

	tmpPath := cleanPath + compactTempSuffix
	backupPath := cleanPath + compactBackupSuffix
	markerPath := cleanPath + compactMarkerSuffix
	// 任何失败路径都要清掉半成品，否则残留文件会挡住下一次 VACUUM INTO。
	cleanupTmp := func() {
		for _, p := range []string{tmpPath, tmpPath + "-wal", tmpPath + "-shm"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("store: 清理预整理临时文件 %s 失败: %v", p, err)
			}
		}
	}
	// 临时文件必须由本次创建；已存在的同名路径（尤其是非空目录）直接报错，不去覆盖它。
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove stale compact temp %s: %w", tmpPath, err)
	}

	// 此时 store 还没打开，这里自己拿一条连接：PRAGMA auto_vacuum 写入的 nextAutovac
	// 保存在连接上，只有紧随其后、同一条连接上的 VACUUM INTO 才会沿用。
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", cleanPath))
	if err != nil {
		return false, fmt.Errorf("open db %s for compact: %w", cleanPath, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("connect db %s for compact: %w", cleanPath, err)
	}

	readPragma := func(name string) (int64, error) {
		var v int64
		if err := conn.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
			return 0, fmt.Errorf("read pragma %s: %w", name, err)
		}
		return v, nil
	}
	drainPragma := func(query string) error {
		rows, err := conn.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("exec pragma %q: %w", query, err)
		}
		defer rows.Close()
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("exec pragma %q: %w", query, err)
		}
		return nil
	}

	mode, err := readPragma("auto_vacuum")
	if err != nil {
		return false, err
	}
	if mode == autoVacuumIncremental {
		if info.Size() < compactRewriteMinBytes {
			return false, nil
		}
		marker, statErr := os.Stat(markerPath)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return false, fmt.Errorf("stat compact marker %s: %w", markerPath, statErr)
		}
		if statErr == nil && time.Since(marker.ModTime()) < compactRewriteInterval {
			return false, nil
		}
	}
	// 先截断 WAL：page_count/freelist_count 才和磁盘上的主文件一致，替换后也不会留下旧 WAL。
	if err := drainPragma("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return false, err
	}
	pageCount, err := readPragma("page_count")
	if err != nil {
		return false, err
	}
	freelist, err := readPragma("freelist_count")
	if err != nil {
		return false, err
	}
	pageSize, err := readPragma("page_size")
	if err != nil {
		return false, err
	}
	// VACUUM INTO 只写有效页，free page 不会进入新库，用这部分字节数预估输出最接近实际。
	total := (pageCount - freelist) * pageSize
	if total < 0 {
		total = 0
	}

	var lastDone int64
	report := func(done int64) {
		if progress == nil || total <= 0 {
			return
		}
		// 中间值只用于进度条：不超过 total-1，避免提前显示完成；同时保证单调不减。
		if done > total-1 {
			done = total - 1
		}
		if done < 0 {
			done = 0
		}
		if done < lastDone {
			done = lastDone
		}
		lastDone = done
		progress(done, total)
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return false, fmt.Errorf("set auto_vacuum to incremental: %w", err)
	}

	stopPoll := make(chan struct{})
	var pollWG sync.WaitGroup
	if progress != nil && total > 0 {
		pollWG.Add(1)
		go func() {
			defer pollWG.Done()
			ticker := time.NewTicker(compactProgressInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopPoll:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					if fi, statErr := os.Stat(tmpPath); statErr == nil {
						report(fi.Size())
					}
				}
			}
		}()
	}

	_, vacuumErr := conn.ExecContext(ctx, "VACUUM INTO ?", tmpPath)
	close(stopPoll)
	pollWG.Wait()
	if vacuumErr != nil {
		cleanupTmp()
		return false, fmt.Errorf("vacuum into %s: %w", tmpPath, vacuumErr)
	}
	if progress != nil {
		progress(total, total)
	}

	// 先彻底放掉源库连接（conn.Close 只是归还连接池，db.Close 才会关闭文件句柄），
	// 再以只读方式校验新文件，避免替换时源库仍开着旧文件。
	if err := conn.Close(); err != nil {
		cleanupTmp()
		return false, fmt.Errorf("close source db %s: %w", cleanPath, err)
	}
	if err := db.Close(); err != nil {
		cleanupTmp()
		return false, fmt.Errorf("close source db handle %s: %w", cleanPath, err)
	}
	compacted, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", tmpPath))
	if err != nil {
		cleanupTmp()
		return false, fmt.Errorf("open compacted db %s read-only: %w", tmpPath, err)
	}
	compacted.SetMaxOpenConns(1)
	var (
		compactedMode int64
		checkResult   string
	)
	verifyErr := compacted.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&compactedMode)
	if verifyErr == nil {
		verifyErr = compacted.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&checkResult)
	}
	closeErr := compacted.Close()
	if verifyErr != nil {
		cleanupTmp()
		return false, fmt.Errorf("verify compacted db %s: %w", tmpPath, verifyErr)
	}
	if closeErr != nil {
		cleanupTmp()
		return false, fmt.Errorf("close compacted db %s: %w", tmpPath, closeErr)
	}
	if compactedMode != autoVacuumIncremental {
		cleanupTmp()
		log.Printf("store: VACUUM INTO 未沿用增量 auto_vacuum（新库实际为 %d），保留原库走原地整理", compactedMode)
		return false, nil
	}
	if checkResult != "ok" {
		cleanupTmp()
		return false, fmt.Errorf("compacted db %s quick_check 未通过: %s", tmpPath, checkResult)
	}

	// 校验通过才动原库：先备份，再把新库就位；第二步失败就回滚备份，尽量不留残局。
	if err := os.Rename(cleanPath, backupPath); err != nil {
		cleanupTmp()
		return false, fmt.Errorf("move db %s to %s: %w", cleanPath, backupPath, err)
	}
	if err := os.Rename(tmpPath, cleanPath); err != nil {
		if restoreErr := os.Rename(backupPath, cleanPath); restoreErr != nil {
			// 回滚也失败时保留两份文件，便于人工恢复，不再删临时文件。
			log.Printf("store: 预整理替换失败且回滚失败，原库在 %s，新库在 %s: %v", backupPath, tmpPath, restoreErr)
			return false, fmt.Errorf("move compacted db %s into place: %w (restore original db: %v)", tmpPath, err, restoreErr)
		}
		cleanupTmp()
		return false, fmt.Errorf("move compacted db %s into place: %w", tmpPath, err)
	}
	// 原库的 -wal/-shm 属于旧文件，新库就位后必须删掉，否则下次打开会把旧 WAL 套到新库上。
	for _, stale := range []string{cleanPath + "-wal", cleanPath + "-shm", backupPath} {
		if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("store: 清理 %s 失败: %v", stale, err)
		}
	}
	if markerErr := os.WriteFile(markerPath, nil, 0o600); markerErr != nil {
		log.Printf("store: 写入整理标记 %s 失败: %v", markerPath, markerErr)
	} else {
		now := time.Now()
		if markerErr := os.Chtimes(markerPath, now, now); markerErr != nil {
			log.Printf("store: 刷新整理标记 %s 时间失败: %v", markerPath, markerErr)
		}
	}

	afterBytes := int64(0)
	if fi, statErr := os.Stat(cleanPath); statErr == nil {
		afterBytes = fi.Size()
	}
	log.Printf("store: 已用 VACUUM INTO 预整理数据库，耗时 %s，%d -> %d 字节",
		time.Since(started).Round(time.Millisecond), info.Size(), afterBytes)
	return true, nil
}

// EnsureIncrementalAutoVacuum 删除重复索引并把库切到增量 auto_vacuum。
// 返回 converted 表示本次是否真的做了切换（VACUUM）；已是增量模式时直接返回 false。
func (s *Store) EnsureIncrementalAutoVacuum(ctx context.Context) (converted bool, err error) {
	// idx_messages_thread_seq 与表定义里 UNIQUE(thread_id, seq) 生成的自动索引完全重复，
	// 只会多占一份索引空间、多一份写入维护开销；老库由早期迁移建出，新库建出后也在这里删掉。
	if _, err := s.db.ExecContext(ctx, "DROP INDEX IF EXISTS idx_messages_thread_seq"); err != nil {
		return false, fmt.Errorf("drop redundant messages index: %w", err)
	}

	mode, err := s.pragmaInt(ctx, "auto_vacuum")
	if err != nil {
		return false, err
	}
	if mode == autoVacuumIncremental {
		return false, nil
	}

	started := time.Now()
	before, err := s.pragmaInt(ctx, "page_count")
	if err != nil {
		return false, err
	}

	// auto_vacuum 的新值要等整库重建才落到文件头，所以改完必须紧跟一次 VACUUM。
	if _, err := s.db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return false, fmt.Errorf("set auto_vacuum to incremental: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return false, fmt.Errorf("vacuum db: %w", err)
	}
	// VACUUM 把整库副本写进了 WAL，不截断的话文件会短暂变大。
	if err := s.execPragmaDrain(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return false, err
	}

	mode, err = s.pragmaInt(ctx, "auto_vacuum")
	if err != nil {
		return false, err
	}
	if mode != autoVacuumIncremental {
		return false, fmt.Errorf("auto_vacuum 切换后仍为 %d，期望 %d", mode, autoVacuumIncremental)
	}
	after, err := s.pragmaInt(ctx, "page_count")
	if err != nil {
		return false, err
	}
	log.Printf("store: 数据库已切换为增量 auto_vacuum，耗时 %s，page_count %d -> %d",
		time.Since(started).Round(time.Millisecond), before, after)
	return true, nil
}

// ReclaimFreePages 分批把 freelist 里的空闲页还给文件系统，返回回收的页数。
// 库不是增量 auto_vacuum 模式时什么都不做（返回 0）；可被 ctx 中断。
func (s *Store) ReclaimFreePages(ctx context.Context, batchPages int, pause time.Duration) (reclaimed int64, err error) {
	if batchPages <= 0 {
		batchPages = defaultReclaimBatchPages
	}
	if pause <= 0 {
		pause = defaultReclaimPause
	}

	mode, err := s.pragmaInt(ctx, "auto_vacuum")
	if err != nil {
		return 0, err
	}
	// 非增量模式没有 incremental_vacuum 可言，直接跳过，不给调用方制造错误。
	if mode != autoVacuumIncremental {
		return 0, nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return reclaimed, err
		}
		free, err := s.pragmaInt(ctx, "freelist_count")
		if err != nil {
			return reclaimed, err
		}
		if free == 0 {
			break
		}
		if err := s.execPragmaDrain(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", batchPages)); err != nil {
			return reclaimed, err
		}
		after, err := s.pragmaInt(ctx, "freelist_count")
		if err != nil {
			return reclaimed, err
		}
		if after >= free {
			// 没有进展就退出，避免参数异常时空转。
			break
		}
		reclaimed += int64(free - after)
		if after == 0 {
			break
		}

		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return reclaimed, ctx.Err()
		case <-timer.C:
		}
	}

	if reclaimed > 0 {
		if err := s.execPragmaDrain(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return reclaimed, err
		}
	}
	return reclaimed, nil
}

// pragmaInt 读取返回单个整数的 PRAGMA（auto_vacuum、page_count、freelist_count）。
func (s *Store) pragmaInt(ctx context.Context, name string) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, fmt.Errorf("read pragma %s: %w", name, err)
	}
	return v, nil
}

// execPragmaDrain 执行可能返回结果行的 PRAGMA（wal_checkpoint、incremental_vacuum），
// 把结果集迭代到底再关闭：连接池只有一条连接，残留 rows 会卡住后续查询。
func (s *Store) execPragmaDrain(ctx context.Context, query string) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("exec pragma %q: %w", query, err)
	}
	defer rows.Close()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("exec pragma %q: %w", query, err)
	}
	return nil
}
