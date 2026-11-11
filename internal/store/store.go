package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"mcode/internal/startuptrace"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示请求的资源不存在。
var ErrNotFound = errors.New("not found")

// ErrRevisionConflict 表示基于版本号的乐观并发控制（CAS）发生冲突。
var ErrRevisionConflict = errors.New("revision conflict")

// 注意：下面这些 SQLite 物理表名与列名（hydra_leads、heads_json、heads_provider/heads_model/heads_effort、
// head_tiers、hydra_pair_id、hydra_mode 等）是兼容历史库保留的旧名，不能跟着改名；
// Go 层已统一改名为 Swarm / bee，只有落盘的字面名称保持原样。
const swarmLeadsMigration = `
	CREATE TABLE IF NOT EXISTS hydra_leads (
		lead_thread_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0,
		project_id TEXT NOT NULL DEFAULT '',
		lead_work_dir TEXT NOT NULL DEFAULT '',
		next_index INTEGER NOT NULL DEFAULT 0,
		heads_json TEXT NOT NULL DEFAULT '[]',
		updated_at TEXT NOT NULL
	);
`

// migrations 定义按版本顺序执行的数据库迁移脚本；新迁移只能追加到末尾，不能插入中间。
var migrations = []string{
	`
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS threads (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		title TEXT NOT NULL,
		provider TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		archived INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		role TEXT NOT NULL,
		kind TEXT NOT NULL,
		text TEXT NOT NULL,
		turn_index INTEGER NOT NULL,
		turn_id TEXT NOT NULL DEFAULT '',
		is_final INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		UNIQUE(thread_id, seq)
	);

	CREATE INDEX IF NOT EXISTS idx_threads_project_updated ON threads(project_id, updated_at);
	CREATE INDEX IF NOT EXISTS idx_messages_thread_seq ON messages(thread_id, seq);
	`,
	`
	CREATE TABLE IF NOT EXISTS turn_usage (
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		turn_index INTEGER NOT NULL,
		input_tokens INTEGER NOT NULL,
		output_tokens INTEGER NOT NULL,
		cached_tokens INTEGER NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(thread_id, turn_index)
	);

	CREATE INDEX IF NOT EXISTS idx_turn_usage_thread ON turn_usage(thread_id, turn_index);
	`,
	`
	ALTER TABLE threads ADD COLUMN runtime_mode TEXT NOT NULL DEFAULT 'fullAccess';
	`,
	`
	ALTER TABLE threads ADD COLUMN model TEXT NOT NULL DEFAULT '';
	`,
	`
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		thread_id TEXT,
		review_thread_id TEXT,
		goal_id TEXT,
		depends_on TEXT NOT NULL DEFAULT '[]',
		title TEXT NOT NULL,
		notes TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'todo',
		priority TEXT NOT NULL DEFAULT 'none',
		review_summary TEXT,
		review_feedback TEXT,
		review_status TEXT,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		last_failure_reason TEXT,
		last_activity_text TEXT,
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS goals (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		title TEXT NOT NULL,
		prompt TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'planning',
		policy TEXT NOT NULL DEFAULT '{}',
		progress TEXT NOT NULL DEFAULT '{}',
		plan_thread_id TEXT,
		plan_summary TEXT,
		status_reason TEXT,
		result_summary TEXT,
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS goal_tasks (
		goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		position INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(goal_id, task_id)
	);

	CREATE TABLE IF NOT EXISTS automations (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		name TEXT NOT NULL,
		prompt TEXT NOT NULL,
		schedule TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL DEFAULT 'active',
		next_run_at TEXT,
		last_run_at TEXT,
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS automation_runs (
		id TEXT PRIMARY KEY,
		automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
		thread_id TEXT,
		status TEXT NOT NULL,
		started_at TEXT NOT NULL,
		finished_at TEXT,
		result_summary TEXT,
		error TEXT,
		revision INTEGER NOT NULL DEFAULT 1
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_project_updated ON tasks(project_id, updated_at);
	CREATE INDEX IF NOT EXISTS idx_tasks_goal ON tasks(goal_id);
	CREATE INDEX IF NOT EXISTS idx_goals_project ON goals(project_id);
	CREATE INDEX IF NOT EXISTS idx_automation_runs_auto_started ON automation_runs(automation_id, started_at);
	`,
	`
	ALTER TABLE goal_tasks ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
	`,
	`
	ALTER TABLE threads ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE threads ADD COLUMN follow_up_revision INTEGER NOT NULL DEFAULT 0;
	`,
	`
	CREATE TABLE IF NOT EXISTS thread_follow_ups (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		text TEXT NOT NULL,
		attachments TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_thread_follow_ups_thread ON thread_follow_ups(thread_id, position);
	`,
	swarmLeadsMigration,
	`
	ALTER TABLE tasks ADD COLUMN is_draft INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE goals ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
	`,
	`
	ALTER TABLE tasks ADD COLUMN goal_prompt TEXT;
	`,
	// MCP 分支曾独立使用 v9/v10；补建其版本号已跳过的 Swarm 表。
	swarmLeadsMigration,
	`
	ALTER TABLE messages ADD COLUMN media TEXT NOT NULL DEFAULT '';
	ALTER TABLE threads ADD COLUMN continuation TEXT NOT NULL DEFAULT '';
	ALTER TABLE thread_follow_ups ADD COLUMN attachment_meta TEXT NOT NULL DEFAULT '';
	`,
	`
	ALTER TABLE threads ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
	`,
	`
	ALTER TABLE threads ADD COLUMN interaction_mode TEXT NOT NULL DEFAULT 'build';
	ALTER TABLE threads ADD COLUMN effort TEXT NOT NULL DEFAULT '';
	ALTER TABLE threads ADD COLUMN heads_provider TEXT NOT NULL DEFAULT '';
	ALTER TABLE threads ADD COLUMN heads_model TEXT NOT NULL DEFAULT '';
	ALTER TABLE threads ADD COLUMN heads_effort TEXT NOT NULL DEFAULT '';
	`,
	`
	ALTER TABLE hydra_leads ADD COLUMN plans_only INTEGER;
	`,
	// 单个事务中执行多语句 ALTER 时若首句失败后续语句不会执行；为保证旧库增量迁移幂等，每列单独一个迁移版本。
	`ALTER TABLE threads ADD COLUMN is_settled INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE threads ADD COLUMN settled_at TEXT;`,
	`ALTER TABLE threads ADD COLUMN is_provisional INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE threads ADD COLUMN worktree_path TEXT;`,
	`ALTER TABLE threads ADD COLUMN branch TEXT;`,
	`ALTER TABLE threads ADD COLUMN worktree_requested INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE threads ADD COLUMN hydra_pair_id TEXT;`,
	`ALTER TABLE threads ADD COLUMN head_tiers TEXT;`,
	`ALTER TABLE threads ADD COLUMN hydra_mode TEXT NOT NULL DEFAULT 'inherited';`,
	`ALTER TABLE threads ADD COLUMN parent_thread_id TEXT;`,
	`ALTER TABLE threads ADD COLUMN is_helper INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE threads ADD COLUMN has_unread INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE threads ADD COLUMN last_status TEXT;`,
	`ALTER TABLE threads ADD COLUMN provider_resume_at TEXT;`,
	`ALTER TABLE threads ADD COLUMN provider_anchor TEXT;`,
	`
	CREATE INDEX IF NOT EXISTS idx_threads_parent ON threads(parent_thread_id);
	CREATE INDEX IF NOT EXISTS idx_threads_settled ON threads(is_settled, settled_at);

	CREATE TABLE IF NOT EXISTS turns (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		turn_index INTEGER NOT NULL,
		status TEXT NOT NULL,
		started_at TEXT NOT NULL,
		completed_at TEXT,
		base_checkpoint TEXT,
		finish_checkpoint TEXT,
		user_prompt TEXT NOT NULL,
		system_injected_context TEXT,
		raw_output TEXT,
		touched_files TEXT,
		input_tokens INTEGER,
		output_tokens INTEGER,
		cost_micros INTEGER,
		error_message TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_turns_thread ON turns(thread_id, turn_index);
	`,
	`ALTER TABLE automation_runs ADD COLUMN changed_files INTEGER;`,
	// hydra_leads 的委派轮次与请求开始时间；沿用每列独立迁移版本的幂等写法。
	`ALTER TABLE hydra_leads ADD COLUMN delegation_rounds INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE hydra_leads ADD COLUMN request_started_at TEXT;`,
	`ALTER TABLE threads ADD COLUMN fast_mode INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE threads ADD COLUMN speed_tier TEXT NOT NULL DEFAULT '';`,
	// C-ST-06：损坏会话隔离表；只记录坏会话与坏消息 ID，原始行不删除，便于以后人工恢复。
	`
	CREATE TABLE IF NOT EXISTS quarantined_threads (
		thread_id TEXT PRIMARY KEY,
		reason TEXT NOT NULL,
		raw_message_ids TEXT NOT NULL DEFAULT '[]',
		quarantined_at TEXT NOT NULL
	);
	`,
	`
	ALTER TABLE messages ADD COLUMN tool_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE messages ADD COLUMN tool_input TEXT NOT NULL DEFAULT '';
	ALTER TABLE messages ADD COLUMN tool_status TEXT NOT NULL DEFAULT '';
	ALTER TABLE messages ADD COLUMN tool_completed_at TEXT NOT NULL DEFAULT '';
	`,
	// 用户最后发消息时间：侧边栏排序依据，重启后仍能保持顺序。
	`ALTER TABLE threads ADD COLUMN last_user_message_at TEXT;`,
	// 应用级键值设置（如演示项目只种一次的标记），与项目/线程无关。
	`
	CREATE TABLE IF NOT EXISTS app_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	`,
	// 此列已废弃不再读写，仅为保持迁移序号保留。
	`ALTER TABLE threads ADD COLUMN hydra_read_only_head INTEGER NOT NULL DEFAULT 0;`,
	// 目标状态 plan_ready 更名为 plan_review，存量数据同步改写。
	`UPDATE goals SET status = 'plan_review' WHERE status = 'plan_ready';`,
	// 编排模式 alwaysHeads 更名为 alwaysBees，存量数据同步改写。
	`UPDATE threads SET hydra_mode = 'alwaysBees' WHERE hydra_mode = 'alwaysHeads';`,
	// 分叉（fork）来源：源会话 ID 与分叉点消息 ID，重启后仍能在时间线顶部显示来源条。
	`ALTER TABLE threads ADD COLUMN forked_from_thread_id TEXT;`,
	`ALTER TABLE threads ADD COLUMN forked_from_message_id TEXT;`,
	// 运行记录已读状态：read_at 为空即未读，新记录默认未读；沿用每列独立迁移版本的幂等写法。
	`ALTER TABLE automation_runs ADD COLUMN read_at TEXT;`,
	// 运行记录归档标记：默认不展示已归档记录。
	`ALTER TABLE automation_runs ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;`,
	// 存量运行记录在首次升级时视为已读，避免上线后历史记录全部变成未读。
	`UPDATE automation_runs SET read_at = started_at WHERE read_at IS NULL;`,
	// 会话备注：每个会话最多一条，线程删除时级联清理。
	`
	CREATE TABLE IF NOT EXISTS thread_notes (
		thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
		text TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	);
	`,
	// 置顶消息：同一会话内同一条消息只保留一行，线程删除时级联清理。
	`
	CREATE TABLE IF NOT EXISTS pinned_messages (
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		message_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (thread_id, message_id)
	);
	`,
	// 编排排队条目：重启前尚未启动的排队 bee 随主会话一起落盘，重启后交回用户决定是否续跑。
	`ALTER TABLE hydra_leads ADD COLUMN queue_json TEXT NOT NULL DEFAULT '[]';`,
	// 每轮最后一次模型请求的上下文大小，供重启后仍能按「单次请求」口径显示上下文窗口占用；
	// 旧库该列默认 0，表示 provider 未上报，界面回退到「输入 + 输出」。
	`ALTER TABLE turn_usage ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0;`,
	// 会话级人设 id：空串表示从未设置（不属于任何预设），"none" 表示用户显式选了无人设。
	`ALTER TABLE threads ADD COLUMN persona TEXT NOT NULL DEFAULT '';`,
	// 错误消息的失败类别（取值同 harness.ErrKind*）：重启后历史里的错误横幅仍能按类别渲染。
	// 空串表示未分类，界面回退到关键词识别。
	`ALTER TABLE messages ADD COLUMN err_kind TEXT NOT NULL DEFAULT '';`,
	// 每轮 CLI 上报的上下文窗口上限与花费：重启后用量面板仍能按实测窗口与本轮费用展示。
	// 旧库两列默认 0，表示 provider 未上报，与现有零值路径行为一致。
	`ALTER TABLE turn_usage ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE turn_usage ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;`,
	// 记忆提议收件箱：agent 只能提议，先落在本地，由用户确认后再写入远端记忆服务。
	// 标签以 JSON 数组文本保存；thread_id 为空表示不是某个会话发起的提议。
	`
	CREATE TABLE IF NOT EXISTS memory_proposals (
		id TEXT PRIMARY KEY,
		project_path TEXT NOT NULL DEFAULT '',
		thread_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		unit_type TEXT NOT NULL DEFAULT 'fact',
		importance REAL NOT NULL DEFAULT 0.5,
		labels_json TEXT NOT NULL DEFAULT '[]',
		source TEXT NOT NULL DEFAULT 'agent',
		status TEXT NOT NULL DEFAULT 'pending',
		remote_memory_id TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_memory_proposals_status ON memory_proposals(status);
	CREATE INDEX IF NOT EXISTS idx_memory_proposals_project_path ON memory_proposals(project_path);
	`,
	// 移除会话备注与置顶消息表。
	`DROP TABLE IF EXISTS thread_notes;`,
	`DROP TABLE IF EXISTS pinned_messages;`,
	// 记忆提议的写入范围（project / global）与改写目标（非空表示更新已有远端记忆）。
	`ALTER TABLE memory_proposals ADD COLUMN scope TEXT NOT NULL DEFAULT 'project';`,
	`ALTER TABLE memory_proposals ADD COLUMN target_memory_id TEXT NOT NULL DEFAULT '';`,
	// 待处理事件表：记录会话卡死、任务失败、待审批等需要用户关注的对象状态，
	// dedupe_key 保证同一对象同一进入时刻只落一条，resolved_at 非空表示已消解。
	`
	CREATE TABLE IF NOT EXISTS attention_events (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		subject_type TEXT NOT NULL,
		subject_id TEXT NOT NULL,
		project_id TEXT NOT NULL DEFAULT '',
		thread_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		entered_at TEXT NOT NULL,
		dedupe_key TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		resolved_at TEXT,
		resolved_by TEXT NOT NULL DEFAULT ''
	);

	CREATE INDEX IF NOT EXISTS idx_attention_events_resolved ON attention_events(resolved_at);
	CREATE INDEX IF NOT EXISTS idx_attention_events_subject ON attention_events(subject_type, subject_id, kind, created_at);
	`,
	// 任务验收标准与打回计数：验收标准为多行文本，每行一条，行首带 [命令]/[审阅]/[你验] 标签。
	`ALTER TABLE tasks ADD COLUMN acceptance_criteria TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE tasks ADD COLUMN reject_count INTEGER NOT NULL DEFAULT 0;`,
	// 目标创建者标记：steward 表示由总管创建，空串表示普通目标。
	`ALTER TABLE goals ADD COLUMN owner TEXT NOT NULL DEFAULT '';`,
	// 跟进项落库时携带的用户消息 Kind（如 host_event）：重启后排队项仍能按原类别落库。
	`ALTER TABLE thread_follow_ups ADD COLUMN kind TEXT NOT NULL DEFAULT '';`,
	swarmReportsMigration,
	// bee 会话消息压缩：正文列清空、压缩字节进 BLOB 列，text_encoding 标记编码。每列一个版本，保证幂等。
	`ALTER TABLE messages ADD COLUMN text_z BLOB;`,
	`ALTER TABLE messages ADD COLUMN tool_input_z BLOB;`,
	`ALTER TABLE messages ADD COLUMN text_encoding TEXT NOT NULL DEFAULT '';`,
	// 排队消息生命周期审计：append-only 事件表，event_key 由 store 重算并用 UNIQUE 吸收重放。
	`
	CREATE TABLE IF NOT EXISTS thread_follow_up_events (
		id TEXT PRIMARY KEY,
		event_key TEXT NOT NULL UNIQUE,
		follow_up_id TEXT NOT NULL,
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		kind TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		source_id TEXT NOT NULL DEFAULT '',
		text TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_follow_up_events_thread
		ON thread_follow_up_events(thread_id, created_at);
	CREATE INDEX IF NOT EXISTS idx_follow_up_events_follow_up
		ON thread_follow_up_events(follow_up_id);
	`,
	// 自动化运行来源与补跑计数：trigger 取 scheduled/catch_up/manual，missed_count 记录本次补跑折叠的错过点数。
	`ALTER TABLE automation_runs ADD COLUMN "trigger" TEXT NOT NULL DEFAULT 'scheduled';`,
	`ALTER TABLE automation_runs ADD COLUMN missed_count INTEGER NOT NULL DEFAULT 0;`,
	// 会话级外部 MCP 禁用名单：JSON 数组文本，空表示沿用全局设置。
	`ALTER TABLE threads ADD COLUMN mcp_disabled TEXT;`,
	// 排队项原始意图（queue/insert）：insert 表示用户本意插入当前回合但被降级成排队，空串视为 queue。
	`ALTER TABLE thread_follow_ups ADD COLUMN intent TEXT NOT NULL DEFAULT '';`,
	// 未结算提问/审批的失效留痕：只用于启动对账与排障，不存答复内容、不提供可答入口。
	// state 取 pending/settled/withdrawn/expired；会话删除时随 threads 级联删除。
	`
	CREATE TABLE IF NOT EXISTS pending_asks (
		request_id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		turn_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		resolution TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		settled_at TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_pending_asks_thread ON pending_asks(thread_id, state);
	`,
	// 排队消息离开队列后的投递状态：队列本身仍只由 thread_follow_ups 表示，本表不存正文。
	// state 取 dispatching/accepted/failed/unknown；会话删除时随 threads 级联删除。
	`
	CREATE TABLE IF NOT EXISTS thread_deliveries (
		message_id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		follow_up_id TEXT NOT NULL DEFAULT '',
		turn_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		attempt INTEGER NOT NULL DEFAULT 1,
		failure TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		settled_at TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_thread_deliveries_state ON thread_deliveries(thread_id, state);
	`,
	// 消息正文 zstd 字典：大量短消息单独压缩收益差，训练共享字典后压缩率明显变好。
	// 字典只增不删：历史压缩行的帧头带字典 ID，删掉就读不回来。
	`
	CREATE TABLE IF NOT EXISTS message_zstd_dicts (
		id INTEGER PRIMARY KEY,
		dict BLOB NOT NULL,
		sample_count INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
	`,
	// 会话冷归档：已归档且长期没有更新的会话，全部消息打包成一个 zstd 压缩的 JSON blob，
	// 从 messages 里删除；读路径透明解包，写路径在同一事务内先回填。会话删除时随 threads 级联删除。
	`
	CREATE TABLE IF NOT EXISTS thread_archives (
		thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
		archived_at TEXT NOT NULL,
		message_count INTEGER NOT NULL,
		first_seq INTEGER NOT NULL,
		last_seq INTEGER NOT NULL,
		encoding TEXT NOT NULL,
		payload BLOB NOT NULL,
		raw_bytes INTEGER NOT NULL
	);
	`,
	// 总管审批建议计分：一条总管提过建议的请求一行，登记与结局各写一次；
	// outcome 取 pending/agree/disagree/unanswered，未答复不计入一致率分母。
	`
	CREATE TABLE IF NOT EXISTS steward_approval_scores (
		request_id         TEXT PRIMARY KEY,
		thread_id          TEXT NOT NULL DEFAULT '',
		turn_id            TEXT NOT NULL DEFAULT '',
		kind               TEXT NOT NULL DEFAULT '',
		tool_name          TEXT NOT NULL DEFAULT '',
		suggested_option   TEXT NOT NULL DEFAULT '',
		suggested_role     TEXT NOT NULL DEFAULT '',
		chosen_option      TEXT NOT NULL DEFAULT '',
		chosen_role        TEXT NOT NULL DEFAULT '',
		outcome            TEXT NOT NULL DEFAULT 'pending',
		answer_source      TEXT NOT NULL DEFAULT '',
		unanswered_reason  TEXT NOT NULL DEFAULT '',
		created_at         TEXT NOT NULL,
		updated_at         TEXT NOT NULL,
		answered_at        TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_steward_scores_outcome ON steward_approval_scores(outcome, kind);
	CREATE INDEX IF NOT EXISTS idx_steward_scores_created ON steward_approval_scores(created_at);
	`,
	// 每轮逐次模型请求的速度样本（JSON 数组文本），供重启后用量面板继续展示单次请求速度；
	// 旧库该列默认空串，表示没有样本，界面回退到整轮耗时口径。
	`ALTER TABLE turn_usage ADD COLUMN requests_json TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE tasks ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';`,
	`
	CREATE TABLE IF NOT EXISTS steward_decisions (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL DEFAULT '',
		thread_id TEXT NOT NULL DEFAULT '',
		question TEXT NOT NULL DEFAULT '',
		context TEXT NOT NULL DEFAULT '',
		options_json TEXT NOT NULL DEFAULT '[]',
		recommended TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'open',
		answer TEXT NOT NULL DEFAULT '',
		answered_by TEXT NOT NULL DEFAULT '',
		answered_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_steward_decisions_task ON steward_decisions(task_id);
	CREATE INDEX IF NOT EXISTS idx_steward_decisions_status ON steward_decisions(status);
	`,
}

// Store 封装 SQLite 数据库操作。
type Store struct {
	db *sql.DB
	// RecoveredFrom 记录数据库损坏自愈时的备份文件路径；若未发生自愈恢复则为空。
	RecoveredFrom string
	// filePath 是数据库主文件的物理路径，用于后台完整性检查与标记文件。
	filePath string
	// bgCheckCancel 取消后台完整性检查；bgCheckDone 在后台检查退出后关闭。
	bgCheckCancel context.CancelFunc
	bgCheckDone   chan struct{}
	// zstdDictID 是当前写入压缩消息所用的 zstd 字典 ID；0 表示无字典。
	// 保留任务在后台 goroutine 里训练字典并写这个字段，所以读写都用 atomic。
	zstdDictID uint32
}

// isCorruptError 检查错误文本是否包含 SQLite 损坏相关特征（不区分大小写）。
func isCorruptError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "malformed") ||
		strings.Contains(msg, "not a database") ||
		strings.Contains(msg, "corrupt") ||
		strings.Contains(msg, "file is encrypted")
}

// isMemoryDB 检查路径或 DSN 是否指向内存数据库。
func isMemoryDB(path, dsn string) bool {
	lowerPath := strings.ToLower(path)
	lowerDSN := strings.ToLower(dsn)
	return strings.Contains(lowerPath, ":memory:") ||
		strings.Contains(lowerDSN, ":memory:") ||
		strings.Contains(lowerPath, "mode=memory") ||
		strings.Contains(lowerDSN, "mode=memory")
}

// openAndMigrate 尝试打开数据库，并在 syncCheck 为真时执行 quick_check 检查，最后完成建表和迁移。
func openAndMigrate(ctx context.Context, dsn string, syncCheck bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}

	// 常规启动跳过同步 quick_check，只在存在标记文件（或内存库）时同步检查完整性
	if syncCheck {
		if err := runQuickCheck(ctx, db); err != nil {
			_ = db.Close()
			return nil, err
		}
		startuptrace.Mark("store quick_check")
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// 清扫进程异常退出遗留的空临时会话（幂等）。
	if _, err := db.ExecContext(ctx, "DELETE FROM threads WHERE is_provisional = 1 AND NOT EXISTS (SELECT 1 FROM messages WHERE thread_id = threads.id) AND NOT EXISTS (SELECT 1 FROM thread_archives a WHERE a.thread_id = threads.id)"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sweep orphan provisional threads: %w", err)
	}
	startuptrace.Mark("store provisional sweep")

	return db, nil
}

// quickCheckMarkerSuffix 是同步完整性检查标记文件的后缀；标记存在时下次启动必须同步检查。
const quickCheckMarkerSuffix = ".needs-check"

// quickCheckMarkerPath 返回给定数据库主文件的标记文件路径。
func quickCheckMarkerPath(filePath string) string {
	return filePath + quickCheckMarkerSuffix
}

// quickCheckMarkerExists 报告标记文件是否存在。
func quickCheckMarkerExists(filePath string) bool {
	_, err := os.Stat(quickCheckMarkerPath(filePath))
	return err == nil
}

// removeQuickCheckMarker 删除标记文件；文件本就不存在时视为成功。
func removeQuickCheckMarker(filePath string) {
	if err := os.Remove(quickCheckMarkerPath(filePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("store: failed to remove quick_check marker %s: %v", quickCheckMarkerPath(filePath), err)
	}
}

// writeQuickCheckMarker 写入标记文件，让下次启动走同步检查与自愈；内容只作诊断用，读取时只判断存在性。
func writeQuickCheckMarker(filePath string) {
	content := time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(quickCheckMarkerPath(filePath), []byte(content), 0o644); err != nil {
		log.Printf("store: failed to write quick_check marker %s: %v", quickCheckMarkerPath(filePath), err)
	}
}

// scanQuickCheck 执行 PRAGMA quick_check 并判断结果是否为 ok。
func scanQuickCheck(ctx context.Context, db *sql.DB) error {
	var checkResult string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check;").Scan(&checkResult); err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(checkResult), "ok") {
		return fmt.Errorf("quick_check: %s (corrupt)", checkResult)
	}
	return nil
}

// runQuickCheck 是启动时同步检查使用的实现，测试可替换以观察或模拟同步检查。
var runQuickCheck = scanQuickCheck

// backgroundQuickCheck 用独立的只读连接执行 quick_check，不占用主连接；WAL 模式允许并发读。
var backgroundQuickCheck = func(ctx context.Context, filePath string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=query_only(1)&_pragma=busy_timeout(5000)", filePath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open read-only sqlite db: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return scanQuickCheck(ctx, db)
}

// physicalDBPath 从 DSN 路径中提取数据库主文件的物理路径。
func physicalDBPath(cleanPath string) string {
	filePath := cleanPath
	if idx := strings.Index(filePath, "?"); idx != -1 {
		filePath = filePath[:idx]
	}
	return filepath.Clean(filePath)
}

// startBackgroundQuickCheck 在后台对数据库做一次完整性检查。
// 检查失败时只写标记文件，让下次启动走同步检查与自愈，运行中不做隔离。
func (s *Store) startBackgroundQuickCheck(skip bool) {
	if skip || s.filePath == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.bgCheckCancel = cancel
	s.bgCheckDone = done
	go func() {
		defer close(done)
		if err := backgroundQuickCheck(ctx, s.filePath); err != nil {
			// 关闭过程中被取消，检查结果不再处理
			if ctx.Err() != nil {
				return
			}
			log.Printf("store: background quick_check failed (%v), will re-check %s on next start", err, s.filePath)
			writeQuickCheckMarker(s.filePath)
			return
		}
		startuptrace.Mark("store quick_check (bg) done")
	}()
}

// finalizeOpen 组装 Store：同步检查通过则清理标记文件，并启动后台完整性检查。
// 组装完成前先加载 zstd 字典：缺字典会读不回历史压缩消息，此时 Open 必须失败。
func finalizeOpen(ctx context.Context, db *sql.DB, filePath, recoveredPath string, syncChecked, memory bool) (*Store, error) {
	if syncChecked && !memory {
		removeQuickCheckMarker(filePath)
	}
	s := &Store{db: db, RecoveredFrom: recoveredPath, filePath: filePath}
	if err := s.loadMessageDicts(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load message zstd dicts: %w", err)
	}
	s.startBackgroundQuickCheck(memory)
	return s, nil
}

// quarantineCorruptFiles 将损坏的数据库文件以及同名的 -wal、-shm 改名为备份文件。
func quarantineCorruptFiles(basePath, timestamp string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := basePath + suffix
		if _, err := os.Stat(src); err == nil {
			dst := fmt.Sprintf("%s.corrupt-%s", src, timestamp)
			if renameErr := os.Rename(src, dst); renameErr != nil {
				log.Printf("store: failed to rename corrupt file %s to %s: %v", src, dst, renameErr)
			}
		}
	}
}

// Open 打开指定路径的 SQLite 数据库，配置 DSN 参数并执行迁移。
// 常规启动不做同步 quick_check，改为打开后由后台只读连接检查；
// 若存在 .needs-check 标记（上次后台检查失败）则同步检查。
// 若检测到数据库文件损坏且非内存数据库，将自动备份损坏文件并自愈重试一次。
func Open(path string) (*Store, error) {
	cleanPath := strings.TrimPrefix(path, "file:")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", cleanPath)
	filePath := physicalDBPath(cleanPath)
	memory := isMemoryDB(path, dsn)
	// 内存库无法持久化标记文件，始终同步检查（库为空，开销可忽略）
	syncCheck := memory || quickCheckMarkerExists(filePath)

	ctx := context.Background()
	db, err := openAndMigrate(ctx, dsn, syncCheck)
	if err == nil {
		return finalizeOpen(ctx, db, filePath, "", syncCheck, memory)
	}

	// 内存数据库或非损坏类错误不做自愈处理
	if memory || !isCorruptError(err) {
		return nil, err
	}

	timestamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.corrupt-%s", filePath, timestamp)

	// 重命名隔离损坏文件并记录日志
	quarantineCorruptFiles(filePath, timestamp)
	log.Printf("store: sqlite database is corrupt (%v), quarantined to %s", err, backupPath)
	// 隔离后原标记已无意义，重建的新库由启动路径重新判定
	removeQuickCheckMarker(filePath)

	// 重新打开、建表、迁移一次，只重试一次
	retryDB, retryErr := openAndMigrate(ctx, dsn, syncCheck)
	if retryErr != nil {
		return nil, fmt.Errorf("reopen sqlite db after corruption backup (%s): %w", backupPath, retryErr)
	}

	return finalizeOpen(ctx, retryDB, filePath, backupPath, syncCheck, memory)
}

// RecoveredBackupPath 返回本次打开时发生自愈备份的主文件路径；若未发生自愈恢复则返回空字符串。
func (s *Store) RecoveredBackupPath() string {
	if s == nil {
		return ""
	}
	return s.RecoveredFrom
}

// Close 先取消并等待后台完整性检查退出，再关闭底层数据库连接。
func (s *Store) Close() error {
	if s.bgCheckCancel != nil {
		s.bgCheckCancel()
	}
	if s.bgCheckDone != nil {
		<-s.bgCheckDone
	}
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// migrate 执行所有未执行的数据库版本迁移。
func (s *Store) migrate(ctx context.Context) error {
	var currentVersion int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&currentVersion); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	// 库版本高于程序版本时拒绝打开，避免静默跳过导致后续读到缺失字段。
	if currentVersion > len(migrations) {
		return fmt.Errorf("数据库版本高于当前程序版本: db v%d > program v%d", currentVersion, len(migrations))
	}

	for i, sqlScript := range migrations {
		targetVersion := i + 1
		if currentVersion >= targetVersion {
			continue
		}

		if err := s.applyMigration(ctx, sqlScript, targetVersion); err != nil {
			return fmt.Errorf("apply migration v%d: %w", targetVersion, err)
		}
	}
	startuptrace.Mark("store migrations")

	if err := s.ensureThreadColumns(ctx); err != nil {
		return err
	}
	startuptrace.Mark("store ensureThreadColumns")

	err := s.backfillSwarmLeadHelpers(ctx)
	startuptrace.Mark("store backfillSwarmLeadHelpers")
	return err
}

// ensureThreadColumns 修复历史迁移插入中间或多语句迁移跳过后续列导致的缺列。
func (s *Store) ensureThreadColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(threads);")
	if err != nil {
		return fmt.Errorf("read threads columns: %w", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan threads column: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read threads columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close threads columns: %w", err)
	}

	columnDDL := regexp.MustCompile(`ALTER TABLE threads ADD COLUMN (\w+)[^;]*;`)
	for _, script := range migrations {
		for _, match := range columnDDL.FindAllStringSubmatch(script, -1) {
			name := match[1]
			if columns[name] {
				continue
			}
			if _, err := s.db.ExecContext(ctx, match[0]); err != nil {
				return fmt.Errorf("ensure threads column %s: %w", name, err)
			}
			columns[name] = true
		}
	}
	return nil
}

// backfillSwarmLeadHelpers 幂等回填历史 Swarm bee 会话的 parent_thread_id 与 is_helper 标记。
// 遍历 hydra_leads.heads_json 中的 threadId，若对应会话的 parent_thread_id 为空，
// 则设为该行的 lead_thread_id，并把 is_helper 设为 1。
func (s *Store) backfillSwarmLeadHelpers(ctx context.Context) error {
	var tableCount int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'hydra_leads';").Scan(&tableCount)
	if err != nil {
		return fmt.Errorf("check hydra_leads table: %w", err)
	}
	if tableCount == 0 {
		return nil
	}

	rows, err := s.db.QueryContext(ctx, "SELECT lead_thread_id, heads_json FROM hydra_leads;")
	if err != nil {
		return fmt.Errorf("query hydra_leads for backfill: %w", err)
	}
	defer rows.Close()

	type beeRef struct {
		ThreadID string `json:"threadId"`
	}

	type backfillItem struct {
		leadThreadID string
		beeThreadID  string
	}
	var items []backfillItem

	for rows.Next() {
		var leadID, beesJSON string
		if err := rows.Scan(&leadID, &beesJSON); err != nil {
			return fmt.Errorf("scan hydra_leads for backfill: %w", err)
		}
		beesJSON = strings.TrimSpace(beesJSON)
		if beesJSON == "" || beesJSON == "[]" {
			continue
		}
		var bees []beeRef
		if err := json.Unmarshal([]byte(beesJSON), &bees); err != nil {
			continue
		}
		for _, h := range bees {
			if h.ThreadID != "" {
				items = append(items, backfillItem{leadThreadID: leadID, beeThreadID: h.ThreadID})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate hydra_leads for backfill: %w", err)
	}
	_ = rows.Close()

	for _, it := range items {
		_, err := s.db.ExecContext(ctx,
			"UPDATE threads SET parent_thread_id = ?, is_helper = 1 WHERE id = ? AND (parent_thread_id IS NULL OR parent_thread_id = '');",
			it.leadThreadID, it.beeThreadID,
		)
		if err != nil {
			return fmt.Errorf("backfill swarm helper thread %s: %w", it.beeThreadID, err)
		}
	}
	return nil
}

// applyMigration 在单独的事务中执行一次迁移，并更新 user_version。
func (s *Store) applyMigration(ctx context.Context, sqlScript string, version int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, sqlScript); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", version)); err != nil {
		return err
	}

	return tx.Commit()
}

// timeFormat 为定宽纳秒时间戳格式；定宽保证同秒内的时间字符串可直接比较排序。
const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func encodeJSONList[T any](items []T) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeJSONList[T any](s string) ([]T, error) {
	if s == "" {
		return nil, nil
	}
	var res []T
	if err := json.Unmarshal([]byte(s), &res); err != nil {
		return nil, err
	}
	return res, nil
}

// derefString 将 *string 解引用为 string，nil 转为空字符串。
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// stringToPtr 将 string 转为 *string，空字符串返回 nil。
func stringToPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// UpsertProject 插入或更新项目。
func (s *Store) UpsertProject(ctx context.Context, p Project) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}

	query := `
	INSERT INTO projects (id, name, path, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		path = excluded.path,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query,
		p.ID,
		p.Name,
		p.Path,
		formatTime(p.CreatedAt),
		formatTime(p.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert project %s: %w", p.ID, err)
	}
	return nil
}

// ListProjects 获取所有项目，按 updated_at 倒序排列。
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	query := `SELECT id, name, path, created_at, updated_at FROM projects ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0)
	for rows.Next() {
		var p Project
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &createdAtStr, &updatedAtStr); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		if p.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("parse project created_at: %w", err)
		}
		if p.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
			return nil, fmt.Errorf("parse project updated_at: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// messageSelectCols 是 messages 行的读取列清单；ListMessages/ListMessagesTolerant 共用，新增列只改这里。
// text_z/tool_input_z 存 zstd（或旧版 gzip）压缩后的正文，text_encoding 非空时由 scanMessageRow 解压还原明文。
const messageSelectCols = `
	id, thread_id, seq, role, kind, text, turn_index, turn_id, is_final, created_at, media,
	tool_name, tool_input, tool_status, tool_completed_at, err_kind, text_encoding, text_z, tool_input_z
`

const threadSelectCols = `
	id, project_id, title, provider, model, session_id, created_at, updated_at,
	archived, runtime_mode, sort_order, follow_up_revision, continuation, pinned,
	interaction_mode, effort, heads_provider, heads_model, heads_effort, fast_mode, speed_tier,
	is_settled, settled_at, is_provisional, COALESCE(worktree_path, ''), COALESCE(branch, ''),
	worktree_requested, COALESCE(hydra_pair_id, ''), COALESCE(head_tiers, ''), COALESCE(hydra_mode, 'inherited'),
	COALESCE(parent_thread_id, ''), is_helper, has_unread, COALESCE(last_status, ''),
	COALESCE(provider_resume_at, ''), COALESCE(provider_anchor, ''), COALESCE(last_user_message_at, ''),
	COALESCE(forked_from_thread_id, ''), COALESCE(forked_from_message_id, ''), persona, COALESCE(mcp_disabled, '')
`

func scanThread(s rowScanner) (Thread, error) {
	var t Thread
	var createdAtStr, updatedAtStr string
	var lastUserMessageAtStr string
	var dormantAtStr sql.NullString
	var archivedInt, pinnedInt int
	var isDormantInt, isProvisionalInt, worktreeRequestedInt, isHelperInt, hasUnreadInt int
	var fastModeInt int
	var mcpDisabledStr string

	err := s.Scan(
		&t.ID,
		&t.ProjectID,
		&t.Title,
		&t.Provider,
		&t.Model,
		&t.SessionID,
		&createdAtStr,
		&updatedAtStr,
		&archivedInt,
		&t.RuntimeMode,
		&t.SortOrder,
		&t.FollowUpRevision,
		&t.Continuation,
		&pinnedInt,
		&t.InteractionMode,
		&t.Effort,
		&t.BeesProvider,
		&t.BeesModel,
		&t.BeesEffort,
		&fastModeInt,
		&t.SpeedTier,
		&isDormantInt,
		&dormantAtStr,
		&isProvisionalInt,
		&t.WorktreePath,
		&t.Branch,
		&worktreeRequestedInt,
		&t.SwarmPairID,
		&t.BeeTiers,
		&t.SwarmMode,
		&t.ParentThreadID,
		&isHelperInt,
		&hasUnreadInt,
		&t.LastStatus,
		&t.ProviderResumeAt,
		&t.ProviderAnchor,
		&lastUserMessageAtStr,
		&t.ForkedFromThreadID,
		&t.ForkedFromMessageID,
		&t.Persona,
		&mcpDisabledStr,
	)
	if err != nil {
		return Thread{}, err
	}

	if t.CreatedAt, err = parseTime(createdAtStr); err != nil {
		return Thread{}, fmt.Errorf("parse thread created_at: %w", err)
	}
	if t.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
		return Thread{}, fmt.Errorf("parse thread updated_at: %w", err)
	}
	if lastUserMessageAtStr != "" {
		// 解析失败时降级为 nil，回退到 updated_at 排序，不让单行坏数据毁掉整行会话。
		if at, err := parseTime(lastUserMessageAtStr); err == nil {
			t.LastUserMessageAt = &at
		}
	}
	if dormantAtStr.Valid && dormantAtStr.String != "" {
		// settled_at 解析失败时降级为 nil，不让单行坏数据毁掉整行会话
		if st, err := parseTime(dormantAtStr.String); err == nil {
			t.DormantAt = &st
		}
	}

	t.Archived = archivedInt != 0
	t.Pinned = pinnedInt != 0
	t.FastMode = fastModeInt != 0
	t.IsDormant = isDormantInt != 0
	t.IsProvisional = isProvisionalInt != 0
	t.WorktreeRequested = worktreeRequestedInt != 0
	t.IsHelper = isHelperInt != 0
	t.HasUnread = hasUnreadInt != 0
	// 名单解析失败时按空处理，不让单行坏数据毁掉整行会话。
	t.MCPDisabled, _ = decodeJSONList[string](mcpDisabledStr)

	return t, nil
}

// CreateThread 创建新线程。
func (s *Store) CreateThread(ctx context.Context, t Thread) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	boolToInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	runtimeMode := t.RuntimeMode
	if runtimeMode == "" {
		runtimeMode = "fullAccess"
	}
	interactionMode := t.InteractionMode
	if interactionMode == "" {
		interactionMode = "build"
	}
	swarmMode := t.SwarmMode
	if swarmMode == "" {
		swarmMode = "inherited"
	}
	var dormantAt any
	if t.DormantAt != nil {
		dormantAt = formatTime(*t.DormantAt)
	}
	var lastUserMessageAt any
	if t.LastUserMessageAt != nil && !t.LastUserMessageAt.IsZero() {
		lastUserMessageAt = formatTime(*t.LastUserMessageAt)
	}
	mcpDisabled, err := encodeJSONList(t.MCPDisabled)
	if err != nil {
		return fmt.Errorf("encode thread mcp_disabled %s: %w", t.ID, err)
	}

	query := `
	INSERT INTO threads (
		id, project_id, title, provider, model, session_id, created_at, updated_at,
		archived, runtime_mode, sort_order, follow_up_revision, continuation, pinned,
		interaction_mode, effort, heads_provider, heads_model, heads_effort, fast_mode, speed_tier,
		is_settled, settled_at, is_provisional, worktree_path, branch, worktree_requested,
		hydra_pair_id, head_tiers, hydra_mode, parent_thread_id, is_helper, has_unread, last_status,
		provider_resume_at, provider_anchor, last_user_message_at, forked_from_thread_id, forked_from_message_id, persona,
		mcp_disabled
	)
	VALUES (
		?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?,
		NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''),
		NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, '')
	)
	`
	_, err = s.db.ExecContext(ctx, query,
		t.ID,
		t.ProjectID,
		t.Title,
		t.Provider,
		t.Model,
		t.SessionID,
		formatTime(t.CreatedAt),
		formatTime(t.UpdatedAt),
		boolToInt(t.Archived),
		runtimeMode,
		t.SortOrder,
		t.FollowUpRevision,
		t.Continuation,
		boolToInt(t.Pinned),
		interactionMode,
		t.Effort,
		t.BeesProvider,
		t.BeesModel,
		t.BeesEffort,
		boolToInt(t.FastMode),
		t.SpeedTier,
		boolToInt(t.IsDormant),
		dormantAt,
		boolToInt(t.IsProvisional),
		t.WorktreePath,
		t.Branch,
		boolToInt(t.WorktreeRequested),
		t.SwarmPairID,
		t.BeeTiers,
		swarmMode,
		t.ParentThreadID,
		boolToInt(t.IsHelper),
		boolToInt(t.HasUnread),
		t.LastStatus,
		t.ProviderResumeAt,
		t.ProviderAnchor,
		lastUserMessageAt,
		t.ForkedFromThreadID,
		t.ForkedFromMessageID,
		t.Persona,
		mcpDisabled,
	)
	if err != nil {
		return fmt.Errorf("create thread %s: %w", t.ID, err)
	}
	return nil
}

// GetThread 根据 ID 获取线程，不存在时返回 ErrNotFound。
func (s *Store) GetThread(ctx context.Context, id string) (Thread, error) {
	query := "SELECT " + threadSelectCols + " FROM threads WHERE id = ?"
	t, err := scanThread(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Thread{}, ErrNotFound
		}
		return Thread{}, fmt.Errorf("get thread %s: %w", id, err)
	}
	return t, nil
}

// ListThreads 查询线程列表，按 updated_at 倒序排列。projectID 为空表示查询全部。
func (s *Store) ListThreads(ctx context.Context, projectID string, includeArchived bool) ([]Thread, error) {
	query := "SELECT " + threadSelectCols + " FROM threads WHERE 1=1"
	var args []any
	if projectID != "" {
		query += " AND project_id = ?"
		args = append(args, projectID)
	}
	if !includeArchived {
		query += " AND archived = 0"
	}
	query += " ORDER BY updated_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	defer rows.Close()

	threads := make([]Thread, 0)
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate threads: %w", err)
	}
	return threads, nil
}

// ListThreadsTolerant 查询全部线程（含归档），语义与 ListThreads(ctx, "", true) 相同。
// 单个线程行解码失败时不会中止：能读出 id 的记入第二个返回值（损坏会话 ID），其余线程照常返回。
// 只有 SQL 查询本身失败才返回 error。
func (s *Store) ListThreadsTolerant(ctx context.Context) ([]Thread, []string, error) {
	// 快路径：数据完好时一次查询即可，避免启动时对每个会话多一次 GetThread。
	if threads, err := s.ListThreads(ctx, "", true); err == nil {
		return threads, nil, nil
	}
	// 慢路径：先只取 id，再逐行 GetThread；这样单行 scanThread 失败时仍能定位到坏会话的 id。
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM threads ORDER BY updated_at DESC")
	if err != nil {
		return nil, nil, fmt.Errorf("list threads: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			// 连 id 都读不出的行无法定位，只能跳过
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate threads: %w", err)
	}

	threads := make([]Thread, 0, len(ids))
	var badIDs []string
	for _, id := range ids {
		t, err := s.GetThread(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// 查询与读取之间被删除的行不算损坏
				continue
			}
			badIDs = append(badIDs, id)
			continue
		}
		threads = append(threads, t)
	}
	return threads, badIDs, nil
}

// ListArchivedThreads 查询已归档线程列表（archived = 1），按 updated_at 倒序排列。projectID 为空表示查询全部。
func (s *Store) ListArchivedThreads(ctx context.Context, projectID string) ([]Thread, error) {
	query := "SELECT " + threadSelectCols + " FROM threads WHERE archived = 1"
	var args []any
	if projectID != "" {
		query += " AND project_id = ?"
		args = append(args, projectID)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list archived threads: %w", err)
	}
	defer rows.Close()

	threads := make([]Thread, 0)
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate archived threads: %w", err)
	}
	return threads, nil
}

// UpdateThreadSession 更新线程的 sessionID。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadSession(ctx context.Context, id, sessionID string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET session_id = ? WHERE id = ?", sessionID, id)
	if err != nil {
		return fmt.Errorf("update thread session %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadLastStatus 记录线程最近一轮的结束状态（completed、failed、interrupted 等）。
// 侧边栏对未选中的会话只能拿到摘要，重启后靠这一列还原终态。
func (s *Store) UpdateThreadLastStatus(ctx context.Context, id, status string) error {
	if status == "" {
		return nil
	}
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET last_status = ? WHERE id = ?", status, id)
	if err != nil {
		return fmt.Errorf("update thread last status %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchThread 更新线程的 updated_at 时间戳。若线程不存在返回 ErrNotFound。
func (s *Store) TouchThread(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET updated_at = ? WHERE id = ?", formatTime(at), id)
	if err != nil {
		return fmt.Errorf("touch thread %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadTitle 更新线程的 title。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadTitle(ctx context.Context, id, title string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET title = ? WHERE id = ?", title, id)
	if err != nil {
		return fmt.Errorf("update thread title %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadWorkDir 更新线程的工作目录（worktree_path）与 worktree_requested 标记，并清空 session。
// 若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadWorkDir(ctx context.Context, id, workDir string, worktreeRequested bool) error {
	requested := 0
	if worktreeRequested {
		requested = 1
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE threads SET worktree_path = NULLIF(?, ''), worktree_requested = ?, session_id = '' WHERE id = ?",
		workDir, requested, id)
	if err != nil {
		return fmt.Errorf("update thread workdir %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadContinuation 更新线程的 continuation。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadContinuation(ctx context.Context, id, text string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET continuation = ? WHERE id = ?", text, id)
	if err != nil {
		return fmt.Errorf("update thread continuation %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadRuntimeMode 更新线程的 runtime_mode。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadRuntimeMode(ctx context.Context, id, mode string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET runtime_mode = ? WHERE id = ?", mode, id)
	if err != nil {
		return fmt.Errorf("update thread runtime mode %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadComposerPrefs 更新线程的 composer 偏好（交互模式、Lead effort、Bees 供应商/模型/effort、人设 id）。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadComposerPrefs(ctx context.Context, id, interactionMode, effort, beesProvider, beesModel, beesEffort, persona string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE threads SET interaction_mode = ?, effort = ?, heads_provider = ?, heads_model = ?, heads_effort = ?, persona = ? WHERE id = ?",
		interactionMode, effort, beesProvider, beesModel, beesEffort, persona, id)
	if err != nil {
		return fmt.Errorf("update thread composer prefs %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadFastMode 更新线程的快速模式与速率档位。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadFastMode(ctx context.Context, id string, fastMode bool, speedTier string) error {
	fastModeInt := 0
	if fastMode {
		fastModeInt = 1
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE threads SET fast_mode = ?, speed_tier = ? WHERE id = ?",
		fastModeInt, speedTier, id)
	if err != nil {
		return fmt.Errorf("update thread fast mode %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetThreadMCPDisabled 覆盖会话级外部 MCP 禁用名单；空名单表示沿用全局设置。
// 只改名单列，不动 updated_at，避免关开关把会话顶到侧边栏最前。
func (s *Store) SetThreadMCPDisabled(ctx context.Context, id string, keys []string) error {
	encoded, err := encodeJSONList(keys)
	if err != nil {
		return fmt.Errorf("encode thread mcp_disabled %s: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE threads SET mcp_disabled = NULLIF(?, '') WHERE id = ?",
		encoded, id)
	if err != nil {
		return fmt.Errorf("update thread mcp_disabled %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadSwarmMode 更新线程的会话级 Swarm 工作模式（inherited/collaborative/leadPlansOnly/alwaysBees）。
// 若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadSwarmMode(ctx context.Context, id, mode string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE threads SET hydra_mode = ? WHERE id = ?",
		mode, id)
	if err != nil {
		return fmt.Errorf("update thread swarm mode %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadModel 更新线程的 model。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadModel(ctx context.Context, id, model string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET model = ? WHERE id = ?", model, id)
	if err != nil {
		return fmt.Errorf("update thread model %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadProvider 更新线程的 provider。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadProvider(ctx context.Context, id, provider string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET provider = ? WHERE id = ?", provider, id)
	if err != nil {
		return fmt.Errorf("update thread provider %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadSortOrder 更新线程的 sort_order。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadSortOrder(ctx context.Context, id string, sortOrder int64) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET sort_order = ? WHERE id = ?", sortOrder, id)
	if err != nil {
		return fmt.Errorf("update thread sort order %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetThreadArchived 设置线程的归档状态。若线程不存在返回 ErrNotFound。
func (s *Store) SetThreadArchived(ctx context.Context, id string, archived bool) error {
	archivedInt := 0
	if archived {
		archivedInt = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set thread archived %s: %w", id, err)
	}
	defer tx.Rollback()

	// 取消归档时先把冷归档的消息回填，否则用户点开的是一个空会话。
	if !archived {
		if err := s.hydrateThreadTx(ctx, tx, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, "UPDATE threads SET archived = ? WHERE id = ?", archivedInt, id)
	if err != nil {
		return fmt.Errorf("set thread archived %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set thread archived %s: %w", id, err)
	}
	return nil
}

// SetThreadPinned 设置线程的置顶状态。若线程不存在返回 ErrNotFound。
func (s *Store) SetThreadPinned(ctx context.Context, id string, pinned bool) error {
	pinnedInt := 0
	if pinned {
		pinnedInt = 1
	}
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET pinned = ? WHERE id = ?", pinnedInt, id)
	if err != nil {
		return fmt.Errorf("set thread pinned %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetThreadUnread 设置线程的未读状态。若线程不存在返回 ErrNotFound。
func (s *Store) SetThreadUnread(ctx context.Context, id string, hasUnread bool) error {
	unreadInt := 0
	if hasUnread {
		unreadInt = 1
	}
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET has_unread = ? WHERE id = ?", unreadInt, id)
	if err != nil {
		return fmt.Errorf("set thread unread %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetThreadBeeTiers 更新线程的会话级档位覆盖。若线程不存在返回 ErrNotFound。
func (s *Store) SetThreadBeeTiers(ctx context.Context, threadID, beeTiers string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET head_tiers = ? WHERE id = ?", beeTiers, threadID)
	if err != nil {
		return fmt.Errorf("set thread bee tiers %s: %w", threadID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetThreadSwarmPairID 更新线程的 Swarm 配对 ID；空串写 NULL，读取时按空串处理。若线程不存在返回 ErrNotFound。
func (s *Store) SetThreadSwarmPairID(ctx context.Context, threadID, pairID string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET hydra_pair_id = NULLIF(?, '') WHERE id = ?", pairID, threadID)
	if err != nil {
		return fmt.Errorf("set thread swarm pair id %s: %w", threadID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadLifecycle 更新会话生命周期与未读状态（is_settled, settled_at, is_provisional, has_unread）。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadLifecycle(ctx context.Context, id string, isDormant bool, dormantAt *time.Time, isProvisional bool, hasUnread bool) error {
	boolToInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	var dormantAtVal any
	if dormantAt != nil {
		dormantAtVal = formatTime(*dormantAt)
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE threads SET is_settled = ?, settled_at = ?, is_provisional = ?, has_unread = ? WHERE id = ?",
		boolToInt(isDormant), dormantAtVal, boolToInt(isProvisional), boolToInt(hasUnread), id)
	if err != nil {
		return fmt.Errorf("update thread lifecycle %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateThreadRelation 更新线程的父子关系与 helper 标记。若线程不存在返回 ErrNotFound。
func (s *Store) UpdateThreadRelation(ctx context.Context, id, parentThreadID string, isHelper bool) error {
	helperInt := 0
	if isHelper {
		helperInt = 1
	}
	res, err := s.db.ExecContext(ctx, "UPDATE threads SET parent_thread_id = NULLIF(?, ''), is_helper = ? WHERE id = ?", parentThreadID, helperInt, id)
	if err != nil {
		return fmt.Errorf("update thread relation %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteThread 在同一个事务中删除该线程及其关联的 messages、turn usage、follow-ups 等。若线程不存在返回 ErrNotFound。
func (s *Store) DeleteThread(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for delete thread %s: %w", id, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM thread_follow_ups WHERE thread_id = ?", id); err != nil {
		return fmt.Errorf("delete thread_follow_ups: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM turn_usage WHERE thread_id = ?", id); err != nil {
		return fmt.Errorf("delete turn_usage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE thread_id = ?", id); err != nil {
		return fmt.Errorf("delete messages: %w", err)
	}
	// 外键级联已经会删归档行，这里显式删一次，免得历史库的级联被关掉时留下孤儿。
	if _, err := tx.ExecContext(ctx, "DELETE FROM thread_archives WHERE thread_id = ?", id); err != nil {
		return fmt.Errorf("delete thread_archives: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM automation_runs WHERE thread_id = ?", id); err != nil {
		return fmt.Errorf("delete automation_runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET thread_id = NULL WHERE thread_id = ?", id); err != nil {
		return fmt.Errorf("clear tasks thread_id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET review_thread_id = NULL WHERE review_thread_id = ?", id); err != nil {
		return fmt.Errorf("clear tasks review_thread_id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE goals SET plan_thread_id = NULL WHERE plan_thread_id = ?", id); err != nil {
		return fmt.Errorf("clear goals plan_thread_id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM hydra_leads WHERE lead_thread_id = ?", id); err != nil {
		return fmt.Errorf("delete hydra_leads: %w", err)
	}

	res, err := tx.ExecContext(ctx, "DELETE FROM threads WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete thread %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete thread %s: %w", id, err)
	}
	return nil
}

// DeleteProject 删除项目，并在同一事务内依据 threads.project_id 的 ON DELETE CASCADE
// 连带删除该项目的会话及其消息、轮次等依赖行；不删磁盘文件。若项目不存在返回 ErrNotFound。
// 侧边栏只在项目分组里渲染会话，留下悬挂的 threads.project_id 会让这些会话从此不可访问。
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	// PRAGMA foreign_keys 在事务内不生效，必须在开启事务前确保级联已启用。
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 与 DeleteThread 保持一致：会话删除前清掉指向它们的非外键引用（tasks/goals/automation_runs/hydra_leads），
	// 否则级联删除会话后这些列会留下指向不存在会话的 ID。
	threadIDs := "SELECT id FROM threads WHERE project_id = ?"
	cleanups := []struct {
		name  string
		query string
	}{
		{"automation_runs", "DELETE FROM automation_runs WHERE thread_id IN (" + threadIDs + ")"},
		{"tasks.thread_id", "UPDATE tasks SET thread_id = NULL WHERE thread_id IN (" + threadIDs + ")"},
		{"tasks.review_thread_id", "UPDATE tasks SET review_thread_id = NULL WHERE review_thread_id IN (" + threadIDs + ")"},
		{"goals.plan_thread_id", "UPDATE goals SET plan_thread_id = NULL WHERE plan_thread_id IN (" + threadIDs + ")"},
		{"hydra_leads", "DELETE FROM hydra_leads WHERE lead_thread_id IN (" + threadIDs + ")"},
	}
	for _, c := range cleanups {
		if _, err := tx.ExecContext(ctx, c.query, id); err != nil {
			return fmt.Errorf("cleanup %s for project %s: %w", c.name, id, err)
		}
	}

	res, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete project %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete project %s: %w", id, err)
	}
	return nil
}

// AppendMessage 在事务中获取该线程当前最大 seq 并自增，写入新消息并更新线程 updated_at，返回写入后的消息。
// 用户消息同时刷新线程的 last_user_message_at，供侧边栏按“用户最后发消息时间”排序。
func (s *Store) AppendMessage(ctx context.Context, m Message) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 冷归档会话先整体回填，再取 MAX(seq)，保证新 seq 接在历史消息之后。
	if err := s.hydrateThreadTx(ctx, tx, m.ThreadID); err != nil {
		return Message{}, err
	}

	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}

	var maxSeq int64
	row := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM messages WHERE thread_id = ?", m.ThreadID)
	if err := row.Scan(&maxSeq); err != nil {
		return Message{}, fmt.Errorf("query max seq: %w", err)
	}
	m.Seq = maxSeq + 1

	isFinalInt := 0
	if m.IsFinal {
		isFinalInt = 1
	}

	createdAtStr := formatTime(m.CreatedAt)

	mediaJSON, err := encodeJSONList(m.Media)
	if err != nil {
		return Message{}, fmt.Errorf("encode media for message %s: %w", m.ID, err)
	}

	// 更新线程 updated_at；用户消息同时刷新 last_user_message_at，其他角色的消息不改变它。
	lastUserMessageAt := ""
	if m.Role == "user" {
		lastUserMessageAt = createdAtStr
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE threads SET updated_at = ?, last_user_message_at = COALESCE(NULLIF(?, ''), last_user_message_at) WHERE id = ?",
		createdAtStr, lastUserMessageAt, m.ThreadID)
	if err != nil {
		return Message{}, fmt.Errorf("update thread updated_at: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Message{}, err
	}
	if affected == 0 {
		return Message{}, ErrNotFound
	}

	// 插入新消息
	insertQuery := `
	INSERT INTO messages (id, thread_id, seq, role, kind, text, turn_index, turn_id, is_final, created_at, media, tool_name, tool_input, tool_status, tool_completed_at, err_kind)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := tx.ExecContext(ctx, insertQuery,
		m.ID,
		m.ThreadID,
		m.Seq,
		m.Role,
		m.Kind,
		m.Text,
		m.TurnIndex,
		m.TurnID,
		isFinalInt,
		createdAtStr,
		mediaJSON,
		derefString(m.ToolName),
		derefString(m.ToolInput),
		derefString(m.ToolStatus),
		derefString(m.ToolCompletedAt),
		derefString(m.ErrKind),
	); err != nil {
		return Message{}, fmt.Errorf("insert message %s: %w", m.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit tx: %w", err)
	}

	return m, nil
}

// UpdateMessage 按 ID 更新消息的 text、kind、is_final、media、tool_name、tool_input、tool_status、tool_completed_at、err_kind。若消息不存在返回 ErrNotFound。
// 若该行已被 bee 消息保留任务压缩（text_encoding 非空），写回压缩形式，绝不把明文复活到 text/tool_input。
func (s *Store) UpdateMessage(ctx context.Context, m Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := s.updateMessageTx(ctx, tx, m); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update message %s: %w", m.ID, err)
	}
	return nil
}

// updateMessageTx 在调用方的事务内更新一条消息。压缩行的编码判断与写回集中在这里，
// 供 UpdateMessage 与崩溃收尾等需要把消息更新并入更大事务的路径复用。
func (s *Store) updateMessageTx(ctx context.Context, tx *sql.Tx, m Message) error {
	// 消息不在 messages 里说明所在会话已被冷归档：按 ThreadID 整段回填后继续。
	var threadID string
	err := tx.QueryRowContext(ctx, "SELECT thread_id FROM messages WHERE id = ?", m.ID).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		if m.ThreadID != "" {
			if err := s.hydrateThreadTx(ctx, tx, m.ThreadID); err != nil {
				return err
			}
		}
	} else if err != nil {
		return fmt.Errorf("read message %s thread: %w", m.ID, err)
	}

	isFinalInt := 0
	if m.IsFinal {
		isFinalInt = 1
	}

	mediaJSON, err := encodeJSONList(m.Media)
	if err != nil {
		return fmt.Errorf("encode media for message %s: %w", m.ID, err)
	}

	// 先看这一行当前的编码：已压缩的行必须继续以压缩形式写回，否则读路径会拿到空正文或明文与压缩字节打架。
	var currentEncoding string
	err = tx.QueryRowContext(ctx, "SELECT text_encoding FROM messages WHERE id = ?", m.ID).Scan(&currentEncoding)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read message %s encoding: %w", m.ID, err)
	}

	query := `UPDATE messages SET text = ?, kind = ?, is_final = ?, media = ?, tool_name = ?, tool_input = ?, tool_status = ?, tool_completed_at = ?, err_kind = ?, text_z = ?, tool_input_z = ?, text_encoding = ? WHERE id = ?`
	textVal := m.Text
	toolInputVal := derefString(m.ToolInput)
	var textZ, toolInputZ []byte
	newEncoding := ""
	switch currentEncoding {
	case "":
		// 未压缩行：保持原样，顺便清掉可能残留的压缩列与编码。
	case messageEncodingGzip, messageEncodingZstd:
		// 已压缩行：按当前算法重新压缩写回；顺手把老 gzip 行升级成 zstd。
		textZ, toolInputZ, err = encodeMessageContent(textVal, toolInputVal, atomic.LoadUint32(&s.zstdDictID))
		if err != nil {
			return fmt.Errorf("encode message %s content: %w", m.ID, err)
		}
		textVal = ""
		toolInputVal = ""
		newEncoding = messageEncodingZstd
	default:
		// 未知编码（将来换算法）：无法按该算法重新压缩，降级写回明文并清掉编码标记，保证读出来是新内容。
		log.Printf("store: message %s has unknown encoding %q, rewritten as plaintext", m.ID, currentEncoding)
	}

	res, err := tx.ExecContext(ctx, query, textVal, m.Kind, isFinalInt, mediaJSON, derefString(m.ToolName), toolInputVal, derefString(m.ToolStatus), derefString(m.ToolCompletedAt), derefString(m.ErrKind), textZ, toolInputZ, newEncoding, m.ID)
	if err != nil {
		return fmt.Errorf("update message %s: %w", m.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// scanMessageRow 从一行中读出消息基础字段，并返回尚未解析的 created_at 与 media 原始字符串。
// 与 ListMessages、ListMessagesTolerant 共用同一份列顺序（messageSelectCols）。
// text_encoding 非空时（消息保留任务压缩过），在这里解压还原 Text 与 ToolInput，上层拿到的永远是明文。
func scanMessageRow(s rowScanner) (Message, string, string, error) {
	var m Message
	var createdAtStr, mediaStr string
	var toolNameStr, toolInputStr, toolStatusStr, toolCompletedAtStr string
	var errKindStr string
	var textEncodingStr string
	var textZ, toolInputZ []byte
	var isFinalInt int
	if err := s.Scan(
		&m.ID,
		&m.ThreadID,
		&m.Seq,
		&m.Role,
		&m.Kind,
		&m.Text,
		&m.TurnIndex,
		&m.TurnID,
		&isFinalInt,
		&createdAtStr,
		&mediaStr,
		&toolNameStr,
		&toolInputStr,
		&toolStatusStr,
		&toolCompletedAtStr,
		&errKindStr,
		&textEncodingStr,
		&textZ,
		&toolInputZ,
	); err != nil {
		return Message{}, "", "", fmt.Errorf("scan message: %w", err)
	}
	if textEncodingStr != "" {
		text, toolInput, decodeErr := decodeMessageContent(textEncodingStr, textZ, toolInputZ)
		if decodeErr != nil {
			// 不静默：带消息 ID 记日志，再按现有约定交给调用方处理（严格抛错 / 容错跳过）。
			log.Printf("store: decode message %s content: %v", m.ID, decodeErr)
			return Message{}, "", "", &messageContentDecodeError{MessageID: m.ID, Encoding: textEncodingStr, Err: decodeErr}
		}
		m.Text = text
		toolInputStr = toolInput
	}
	m.IsFinal = isFinalInt != 0
	m.ToolName = stringToPtr(toolNameStr)
	m.ToolInput = stringToPtr(toolInputStr)
	m.ToolStatus = stringToPtr(toolStatusStr)
	m.ToolCompletedAt = stringToPtr(toolCompletedAtStr)
	m.ErrKind = stringToPtr(errKindStr)
	return m, createdAtStr, mediaStr, nil
}

// GetMessage 按消息 ID 获取单条消息。
func (s *Store) GetMessage(ctx context.Context, messageID string) (Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+messageSelectCols+` FROM messages WHERE id = ?`, messageID)
	m, createdAt, media, err := scanMessageRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	if m.CreatedAt, err = parseTime(createdAt); err != nil {
		return Message{}, fmt.Errorf("parse message created_at: %w", err)
	}
	if m.Media, err = decodeJSONList[MediaRef](media); err != nil {
		return Message{}, fmt.Errorf("decode media for message %s: %w", m.ID, err)
	}
	return m, nil
}

// ListMessages 分页获取指定线程的消息。
// beforeSeq <= 0 表示从最新消息开始；
// 返回按 seq 升序排列的一页消息，以及布尔值表示是否还有更早的消息。
func (s *Store) ListMessages(ctx context.Context, threadID string, beforeSeq int64, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		return []Message{}, false, nil
	}

	// 冷归档会话没有 messages 行，直接从归档解码后在内存里按同样语义分页。
	if archived, found, err := loadThreadArchive(ctx, s.db, threadID); err != nil {
		return nil, false, fmt.Errorf("load thread archive %s: %w", threadID, err)
	} else if found {
		msgs, hasMore := pageArchivedMessages(archived, beforeSeq, limit)
		return msgs, hasMore, nil
	}

	var query string
	var args []any
	fetchLimit := limit + 1

	if beforeSeq <= 0 {
		query = `
		SELECT ` + messageSelectCols + `
		FROM messages
		WHERE thread_id = ?
		ORDER BY seq DESC
		LIMIT ?
		`
		args = []any{threadID, fetchLimit}
	} else {
		query = `
		SELECT ` + messageSelectCols + `
		FROM messages
		WHERE thread_id = ? AND seq < ?
		ORDER BY seq DESC
		LIMIT ?
		`
		args = []any{threadID, beforeSeq, fetchLimit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var fetched []Message
	for rows.Next() {
		m, createdAtStr, mediaStr, err := scanMessageRow(rows)
		if err != nil {
			return nil, false, err
		}
		if m.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, false, fmt.Errorf("parse message created_at: %w", err)
		}
		if m.Media, err = decodeJSONList[MediaRef](mediaStr); err != nil {
			return nil, false, fmt.Errorf("decode media for message %s: %w", m.ID, err)
		}
		fetched = append(fetched, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate messages: %w", err)
	}

	hasMore := len(fetched) > limit
	if hasMore {
		fetched = fetched[:limit]
	}

	// 翻转为 seq 升序
	for i, j := 0, len(fetched)-1; i < j; i, j = i+1, j-1 {
		fetched[i], fetched[j] = fetched[j], fetched[i]
	}

	if fetched == nil {
		fetched = []Message{}
	}

	return fetched, hasMore, nil
}

// ListMessagesTolerant 与 ListMessages 语义相同，但单行 created_at 解析失败或 media 解码失败时不中止：
// created_at 无法解析时回退为同页前一条（seq 更晚）消息的时间（都没有则用 time.Unix(0,0)），
// media 无法解码时置为 nil，并把这条消息的 ID 记入第三个返回值。
// 只有 SQL 查询本身失败才返回 error。
func (s *Store) ListMessagesTolerant(ctx context.Context, threadID string, beforeSeq int64, limit int) ([]Message, bool, []string, error) {
	if limit <= 0 {
		return []Message{}, false, nil, nil
	}

	// 冷归档会话同样先走归档解码；解码本身已校验完所有行，不再产生 badIDs。
	if archived, found, err := loadThreadArchive(ctx, s.db, threadID); err != nil {
		return nil, false, nil, fmt.Errorf("load thread archive %s: %w", threadID, err)
	} else if found {
		msgs, hasMore := pageArchivedMessages(archived, beforeSeq, limit)
		return msgs, hasMore, nil, nil
	}

	var query string
	var args []any
	fetchLimit := limit + 1

	if beforeSeq <= 0 {
		query = `
		SELECT ` + messageSelectCols + `
		FROM messages
		WHERE thread_id = ?
		ORDER BY seq DESC
		LIMIT ?
		`
		args = []any{threadID, fetchLimit}
	} else {
		query = `
		SELECT ` + messageSelectCols + `
		FROM messages
		WHERE thread_id = ? AND seq < ?
		ORDER BY seq DESC
		LIMIT ?
		`
		args = []any{threadID, beforeSeq, fetchLimit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var fetched []Message
	var badIDs []string
	var lastCreatedAt time.Time
	for rows.Next() {
		m, createdAtStr, mediaStr, err := scanMessageRow(rows)
		if err != nil {
			// 行本身读不出（例如列类型异常或压缩正文解不开）：跳过该行；解压失败时能拿到消息 ID，记入 badIDs 供上层隔离。
			var decodeErr *messageContentDecodeError
			if errors.As(err, &decodeErr) {
				badIDs = append(badIDs, decodeErr.MessageID)
			}
			continue
		}
		if m.CreatedAt, err = parseTime(createdAtStr); err != nil {
			if lastCreatedAt.IsZero() {
				m.CreatedAt = time.Unix(0, 0).UTC()
			} else {
				m.CreatedAt = lastCreatedAt
			}
			badIDs = append(badIDs, m.ID)
		}
		if m.Media, err = decodeJSONList[MediaRef](mediaStr); err != nil {
			m.Media = nil
			// created_at 与 media 同时坏掉时避免重复记录
			if len(badIDs) == 0 || badIDs[len(badIDs)-1] != m.ID {
				badIDs = append(badIDs, m.ID)
			}
		}
		lastCreatedAt = m.CreatedAt
		fetched = append(fetched, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, nil, fmt.Errorf("iterate messages: %w", err)
	}

	hasMore := len(fetched) > limit
	if hasMore {
		fetched = fetched[:limit]
	}

	// 翻转为 seq 升序
	for i, j := 0, len(fetched)-1; i < j; i, j = i+1, j-1 {
		fetched[i], fetched[j] = fetched[j], fetched[i]
	}

	if fetched == nil {
		fetched = []Message{}
	}

	return fetched, hasMore, badIDs, nil
}

// UpsertTurnUsage 插入或覆盖更新指定会话与轮次的用量记录。
func (s *Store) UpsertTurnUsage(ctx context.Context, u TurnUsage) error {
	now := time.Now().UTC()
	query := `
	INSERT INTO turn_usage (thread_id, turn_index, input_tokens, output_tokens, cached_tokens, context_tokens, context_window, cost_usd, requests_json, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(thread_id, turn_index) DO UPDATE SET
		input_tokens = excluded.input_tokens,
		output_tokens = excluded.output_tokens,
		cached_tokens = excluded.cached_tokens,
		context_tokens = excluded.context_tokens,
		context_window = excluded.context_window,
		cost_usd = excluded.cost_usd,
		requests_json = excluded.requests_json,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query,
		u.ThreadID,
		u.TurnIndex,
		u.InputTokens,
		u.OutputTokens,
		u.CachedTokens,
		u.ContextTokens,
		u.ContextWindow,
		u.CostUSD,
		u.RequestsJSON,
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("upsert turn usage for thread %s turn %d: %w", u.ThreadID, u.TurnIndex, err)
	}
	return nil
}

// ListTurnUsage 按 turn_index 升序列出指定会话的所有轮次用量记录。
func (s *Store) ListTurnUsage(ctx context.Context, threadID string) ([]TurnUsage, error) {
	query := `
	SELECT thread_id, turn_index, input_tokens, output_tokens, cached_tokens, context_tokens, context_window, cost_usd, requests_json
	FROM turn_usage
	WHERE thread_id = ?
	ORDER BY turn_index ASC
	`
	rows, err := s.db.QueryContext(ctx, query, threadID)
	if err != nil {
		return nil, fmt.Errorf("list turn usage for thread %s: %w", threadID, err)
	}
	defer rows.Close()

	usages := make([]TurnUsage, 0)
	for rows.Next() {
		var u TurnUsage
		if err := rows.Scan(&u.ThreadID, &u.TurnIndex, &u.InputTokens, &u.OutputTokens, &u.CachedTokens, &u.ContextTokens, &u.ContextWindow, &u.CostUSD, &u.RequestsJSON); err != nil {
			return nil, fmt.Errorf("scan turn usage: %w", err)
		}
		usages = append(usages, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turn usage: %w", err)
	}
	return usages, nil
}

// ListFollowUps 按 position 升序查询指定会话的所有 follow-up 实体。
func (s *Store) ListFollowUps(ctx context.Context, threadID string) ([]FollowUp, error) {
	query := `
	SELECT id, thread_id, position, text, kind, intent, attachments, created_at, updated_at, attachment_meta
	FROM thread_follow_ups
	WHERE thread_id = ?
	ORDER BY position ASC
	`
	rows, err := s.db.QueryContext(ctx, query, threadID)
	if err != nil {
		return nil, fmt.Errorf("list follow ups for thread %s: %w", threadID, err)
	}
	defer rows.Close()

	items := make([]FollowUp, 0)
	for rows.Next() {
		var f FollowUp
		var attachmentsStr, createdAtStr, updatedAtStr, attachmentMetaStr string
		if err := rows.Scan(&f.ID, &f.ThreadID, &f.Position, &f.Text, &f.Kind, &f.Intent, &attachmentsStr, &createdAtStr, &updatedAtStr, &attachmentMetaStr); err != nil {
			return nil, fmt.Errorf("scan follow up: %w", err)
		}
		if attachmentsStr == "" || attachmentsStr == "[]" {
			f.Attachments = []string{}
		} else {
			if err := json.Unmarshal([]byte(attachmentsStr), &f.Attachments); err != nil {
				return nil, fmt.Errorf("unmarshal attachments for follow up %s: %w", f.ID, err)
			}
		}
		if f.AttachmentMeta, err = decodeJSONList[FollowUpAttachment](attachmentMetaStr); err != nil {
			return nil, fmt.Errorf("decode attachment_meta for follow up %s: %w", f.ID, err)
		}
		if f.CreatedAt, err = parseTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("parse follow up created_at: %w", err)
		}
		if f.UpdatedAt, err = parseTime(updatedAtStr); err != nil {
			return nil, fmt.Errorf("parse follow up updated_at: %w", err)
		}
		items = append(items, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate follow ups: %w", err)
	}
	return items, nil
}

// ReplaceFollowUps 保留原签名，审计关闭时等价于 ReplaceFollowUpsWithAudit(..., nil)。
func (s *Store) ReplaceFollowUps(ctx context.Context, threadID string, revision int64, items []FollowUp) error {
	return s.ReplaceFollowUpsWithAudit(ctx, threadID, revision, items, nil)
}

// QuarantinedThread 表示被隔离的损坏会话记录；只记录不删除，原始行保留以便人工恢复。
type QuarantinedThread struct {
	ThreadID      string    `json:"thread_id"`
	Reason        string    `json:"reason"`
	RawMessageIDs []string  `json:"raw_message_ids"`
	QuarantinedAt time.Time `json:"quarantined_at"`
}

// QuarantineThread 记录（或按 thread_id 覆盖更新）一个损坏会话的隔离信息。
func (s *Store) QuarantineThread(ctx context.Context, threadID, reason string, badMessageIDs []string) error {
	idsJSON, err := encodeJSONList(badMessageIDs)
	if err != nil {
		return fmt.Errorf("encode bad message ids for thread %s: %w", threadID, err)
	}
	if idsJSON == "" {
		idsJSON = "[]"
	}
	query := `
	INSERT INTO quarantined_threads (thread_id, reason, raw_message_ids, quarantined_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(thread_id) DO UPDATE SET
		reason = excluded.reason,
		raw_message_ids = excluded.raw_message_ids,
		quarantined_at = excluded.quarantined_at;
	`
	if _, err := s.db.ExecContext(ctx, query, threadID, reason, idsJSON, formatTime(time.Now().UTC())); err != nil {
		return fmt.Errorf("quarantine thread %s: %w", threadID, err)
	}
	return nil
}

// ListQuarantinedThreads 按隔离时间倒序列出所有被隔离的损坏会话。
func (s *Store) ListQuarantinedThreads(ctx context.Context) ([]QuarantinedThread, error) {
	query := `
	SELECT thread_id, reason, raw_message_ids, quarantined_at
	FROM quarantined_threads
	ORDER BY quarantined_at DESC
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list quarantined threads: %w", err)
	}
	defer rows.Close()

	items := make([]QuarantinedThread, 0)
	for rows.Next() {
		var q QuarantinedThread
		var idsStr, atStr string
		if err := rows.Scan(&q.ThreadID, &q.Reason, &idsStr, &atStr); err != nil {
			return nil, fmt.Errorf("scan quarantined thread: %w", err)
		}
		if q.RawMessageIDs, err = decodeJSONList[string](idsStr); err != nil {
			return nil, fmt.Errorf("decode raw_message_ids for thread %s: %w", q.ThreadID, err)
		}
		if q.QuarantinedAt, err = parseTime(atStr); err != nil {
			return nil, fmt.Errorf("parse quarantined_at for thread %s: %w", q.ThreadID, err)
		}
		items = append(items, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quarantined threads: %w", err)
	}
	return items, nil
}
