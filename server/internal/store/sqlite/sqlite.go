// Package sqlite 提供免 CGO 的 SQLite 持久化（modernc.org/sqlite；ADR-02 单二进制）。
//
// 承载 T2.3 服务端核心的持久化语义（docs/02-ARCHITECTURE.md §4 store 行）：
//   - 嵌入式迁移（schema_migrations 版本表）；
//   - 结果/事件/审计/回调 outbox 同事务写入（docs/02 §5.1 不变量②）；
//   - 幂等键（tenant+actor+operation+key，24h 窗口）、设备墓碑（≥7 天）、
//     入向 eventId 去重表；
//   - 崩溃恢复语义（RecoverInflight）：重启后 dispatched/acked/running
//     （= 已投递/已接收/执行中）无持久结果证据 → execution_unknown。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver: sqlite（纯 Go，免 CGO）
)

// ReservedAdminScopeRobotID 为 api_tokens.robot_id 的保留值（admin scope 编码，
// server/internal/admin）。跨包共享单一常量：签发侧（api.IssueOpenAPIToken）与
// 鉴权侧（api.authenticate）都以它硬拒保留名，防止为名叫 admin 的 v1 robot 签发
// 令牌而静默铸出第二个 admin-scope 令牌（CR ADM-1）。W-M5 RBAC 落 scope 列后收编。
const ReservedAdminScopeRobotID = "admin"

// ReservedApproverScopeRobotID 为 api_tokens.robot_id 的第二保留值（approver
// scope 编码，决议面独立身份令牌，P0-3 审批身份车道）。与 admin 同纪律：
// 兼容面签发/鉴权两侧硬拒保留名（防为名叫 approver 的 robot 铸出业务令牌，
// 或决议令牌经 v1:approver 寻址倒灌业务管线——同一枚令牌不得双面行走），
// 常量单一来源两包共享防语义漂移。
const ReservedApproverScopeRobotID = "approver"

// Device 为设备注册行（v2 deviceId 主键；v1 兼容路径以 robot_id 桥接）。
type Device struct {
	DeviceID           string
	RobotID            string
	DeviceName         string
	Ed25519PublicKey   string // hex；重协商凭据续期复用（spec §5.5.1）
	DeviceX25519Public string // hex；设备当前临时公钥（每次配对/重协商更新）
	TokenHash          string // hex；SHA-256(deviceToken)——服务端仅存 hash（spec §5.1.3）
	TokenVersion       int64
	Status             string // active | revoked
	Epoch              int64  // 已分配的最大命令 epoch（单调递增）
	PairedAt           time.Time
	UpdatedAt          time.Time
}

// Session 为 v2 设备会话行。
type Session struct {
	SessionID            string
	DeviceID             string
	ServerX25519Private  string // hex；服务端本轮临时私钥（仅 open/pending 落库供解密派生；closed 即清零，M-4）
	ServerX25519Public   string // hex
	DeviceX25519Public   string // hex；设备本轮临时公钥
	State                string // pending | open | closed
	LastEpoch            int64  // hello 携带的端侧已应用最小 epoch
	LastReceivedSeq      int64  // hello 携带的端侧持久确认最高入向 seq
	EncryptTypeMax       int
	HeartbeatIntervalSec int
	TimeoutSec           int
	CreatedAt            time.Time
}

// Command 为指令行（统一命令管线载体，v1/v2 同表）。
type Command struct {
	CommandID     string
	Target        string // v2 deviceId 或 v1:robotId
	CommandType   int
	Payload       string // list[0] 原文 JSON（下发原样）
	State         string
	Epoch         int64
	Seq           int64 // 最近一次下发分配的会话内出向 seq（重发在新会话重分配）
	EventID       string
	RiskClass     string // L0/L1/L2（docs/01 FR-53 裁决口径；原 L3 别称废止，CR-R1-0）
	DryRun        bool   // FR-54 干跑：同引擎同路径，仅产出报告不下发设备（T3.2）
	CreatedAt     time.Time
	DispatchedAt  sql.NullTime
	AckDeadline   sql.NullTime // dispatched_at + ack_ttl_seconds
	TerminalAt    sql.NullTime
	TerminalState sql.NullString
}

// Approval 为 L2 审批记录（FR-53：L2 需独立 approver 确认；T3.2）。
type Approval struct {
	ApprovalID   string
	Tenant       string
	Actor        string // 申请主体（提交人）；approver 必须与之不同（独立 approver）
	Target       string
	CommandType  int
	ParamsDigest string // sha256(target|type|payload)——审批与实际提交参数绑定
	RiskClass    string
	Status       string // pending | approved | rejected | consumed
	Approver     string // 决议人（须 ≠ Actor）
	Reason       string
	Recurring    bool // 常设审批（cron 周期任务模板；一次性提交为 false）
	CreatedAt    time.Time
	DecidedAt    sql.NullTime
}

// RecipientWhitelist 为 L1 接收人白名单行（FR-53；T3.2）。
type RecipientWhitelist struct {
	Target    string // 统一目标键（v2 deviceId / v1:robotId）
	Recipient string // 接收人标识（载荷 targetRoom，候选字段名 spec §4.4/Q1）
	AddedBy   string
	At        time.Time
}

// APIToken 为对外 REST 兼容面（/openapi/v1）API 令牌行（FR-34；T3.2）。
// 仅存 SHA-256 hash（原始 token 一次返回，与服务端凭据同纪律，spec §5.1.3）。
type APIToken struct {
	TokenHash  string
	RobotID    string
	Tenant     string
	Status     string // active | revoked
	CreatedAt  time.Time
	LastUsedAt sql.NullTime
}

// CronJob 为 cron 定时任务行（FR-38；T3.2）。
type CronJob struct {
	JobID         string
	Name          string
	Tenant        string
	Actor         string
	Target        string
	CommandType   int
	Payload       string // list[0] 原文 JSON（每次触发原样入队）
	CronExpr      string // 5 字段（min hour dom mon dow）
	RiskClass     string // 空 = 按类型分级（scheduler.ClassifyRisk）
	ApprovalID    string // L2 任务须绑定常设审批（Recurring=true）
	Enabled       bool
	LastFireAt    sql.NullTime
	NextFireAt    time.Time
	LastCommandID string // 最近一次触发产生的 commandId（堆积保护判定）
	CreatedAt     time.Time
}

// CommandEvent 为 command_events 行（每条边触发必须留痕，不变量①）。
type CommandEvent struct {
	ID        int64
	CommandID string
	EdgeID    string
	FromState string
	ToState   string
	Event     string
	Detail    string
	At        time.Time
}

// OutboxItem 为回调出站行。
type OutboxItem struct {
	ID          int64
	CommandID   string
	Kind        string // exec_result | report | pair_event
	Payload     string // JSON（仅元数据，不 echo 消息正文，docs/02 §6.1）
	Attempts    int
	NextAttempt time.Time
	Status      string // pending | delivered | dead | skipped
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// HTTPStatus 为最近一次投递尝试的 HTTP 响应码（投递记录独立字段车道，
	// 迁移 v4）。NULL = 从未收到 HTTP 响应（传输错误/尚未投递/skipped——
	// 与「响应码恰为 0」不可能混淆，HTTP 无 0 状态码）。
	HTTPStatus sql.NullInt64
}

// Tombstone 为设备墓碑行（仅元数据）。
type Tombstone struct {
	CommandID  string
	EventID    string
	FinalState string
	TerminalAt time.Time
	PurgeAfter time.Time
}

// InboundEvent 为入向帧 eventId 去重行。
type InboundEvent struct {
	DeviceID      string
	EventID       string
	PayloadDigest string
	Receipt       string
	At            time.Time
}

// schema 为嵌入式 DDL。risk_class 兜底 DEFAULT 'L2'（fail-closed，docs/01 FR-53
// 裁决②）：提交路径（scheduler.Submit）恒显式分级写入，DEFAULT 仅防御库外直插。
const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
  device_id            TEXT PRIMARY KEY,
  robot_id             TEXT UNIQUE,
  device_name          TEXT NOT NULL DEFAULT '',
  ed25519_public_key   TEXT NOT NULL DEFAULT '',
  device_x25519_public TEXT NOT NULL DEFAULT '',
  token_hash           TEXT NOT NULL DEFAULT '',
  token_version        INTEGER NOT NULL DEFAULT 1,
  status               TEXT NOT NULL DEFAULT 'active',
  epoch                INTEGER NOT NULL DEFAULT 0,
  paired_at            INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pairing_codes (
  code_hash  TEXT PRIMARY KEY,
  expires_at INTEGER NOT NULL,
  used_at    INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS device_sessions (
  session_id             TEXT PRIMARY KEY,
  device_id              TEXT NOT NULL,
  server_x25519_private  TEXT NOT NULL,
  server_x25519_public   TEXT NOT NULL,
  device_x25519_public   TEXT NOT NULL,
  state                  TEXT NOT NULL DEFAULT 'pending',
  last_epoch             INTEGER NOT NULL DEFAULT 0,
  last_received_seq      INTEGER NOT NULL DEFAULT 0,
  encrypt_type_max       INTEGER NOT NULL DEFAULT 0,
  created_at             INTEGER NOT NULL,
  opened_at              INTEGER,
  closed_at              INTEGER
);

CREATE TABLE IF NOT EXISTS commands (
  command_id     TEXT PRIMARY KEY,
  target         TEXT NOT NULL,
  command_type   INTEGER NOT NULL,
  payload        TEXT NOT NULL,
  state          TEXT NOT NULL,
  epoch          INTEGER NOT NULL DEFAULT 0,
  seq            INTEGER NOT NULL DEFAULT 0,
  event_id       TEXT NOT NULL DEFAULT '',
  risk_class     TEXT NOT NULL DEFAULT 'L2',
  dry_run        INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  dispatched_at  INTEGER,
  ack_deadline   INTEGER,
  terminal_at    INTEGER,
  terminal_state TEXT
);
CREATE INDEX IF NOT EXISTS idx_commands_state ON commands(state);
CREATE INDEX IF NOT EXISTS idx_commands_target ON commands(target);
CREATE INDEX IF NOT EXISTS idx_commands_target_state ON commands(target, state, created_at);
CREATE INDEX IF NOT EXISTS idx_commands_created ON commands(created_at);
CREATE INDEX IF NOT EXISTS idx_device_sessions_device ON device_sessions(device_id, opened_at);

CREATE TABLE IF NOT EXISTS command_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  command_id TEXT NOT NULL,
  edge_id    TEXT NOT NULL,
  from_state TEXT NOT NULL,
  to_state   TEXT NOT NULL,
  event      TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_command_events_command ON command_events(command_id);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  key         TEXT PRIMARY KEY, -- tenant|actor|operation|key
  params_hash TEXT NOT NULL,
  command_id  TEXT NOT NULL,
  response    TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tombstones (
  command_id   TEXT PRIMARY KEY,
  event_id     TEXT NOT NULL DEFAULT '',
  final_state  TEXT NOT NULL,
  terminal_at  INTEGER NOT NULL,
  purge_after  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS outbox (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  command_id    TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL,
  payload       TEXT NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 0,
  next_attempt  INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL DEFAULT 'pending',
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  http_status   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_outbox_status ON outbox(status, next_attempt);

CREATE TABLE IF NOT EXISTS audit_log (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  kind    TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  detail  TEXT NOT NULL DEFAULT '',
  at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS inbound_events (
  device_id      TEXT NOT NULL,
  event_id       TEXT NOT NULL,
  payload_digest TEXT NOT NULL,
  receipt        TEXT NOT NULL DEFAULT '',
  at             INTEGER NOT NULL,
  PRIMARY KEY (device_id, event_id)
);

-- T3.2（迁移 v2）：FR-53 审批门 / L1 白名单、FR-38 cron、FR-34 兼容面令牌。
CREATE TABLE IF NOT EXISTS approvals (
  approval_id   TEXT PRIMARY KEY,
  tenant        TEXT NOT NULL DEFAULT '',
  actor         TEXT NOT NULL,
  target        TEXT NOT NULL,
  command_type  INTEGER NOT NULL,
  params_digest TEXT NOT NULL,
  risk_class    TEXT NOT NULL DEFAULT 'L2',
  status        TEXT NOT NULL DEFAULT 'pending', -- pending|approved|rejected|consumed
  approver      TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  recurring     INTEGER NOT NULL DEFAULT 0,
  created_ms    INTEGER NOT NULL,
  decided_ms    INTEGER
);

CREATE TABLE IF NOT EXISTS recipient_whitelist (
  target    TEXT NOT NULL,
  recipient TEXT NOT NULL,
  added_by  TEXT NOT NULL DEFAULT '',
  at_ms     INTEGER NOT NULL,
  PRIMARY KEY (target, recipient)
);

-- SR-11（迁移 v3）：外链资源域白名单（G11 阻断项修复——默认零行=任何 fileUrl 拒收）。
CREATE TABLE IF NOT EXISTS allowed_file_domains (
  domain   TEXT PRIMARY KEY,
  added_by TEXT NOT NULL DEFAULT '',
  at_ms    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS api_tokens (
  token_hash   TEXT PRIMARY KEY,
  robot_id     TEXT NOT NULL,
  tenant       TEXT NOT NULL DEFAULT 'default',
  status       TEXT NOT NULL DEFAULT 'active',
  created_ms   INTEGER NOT NULL,
  last_used_ms INTEGER
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_robot ON api_tokens(robot_id);

CREATE TABLE IF NOT EXISTS cron_jobs (
  job_id           TEXT PRIMARY KEY,
  name             TEXT NOT NULL DEFAULT '',
  tenant           TEXT NOT NULL DEFAULT '',
  actor            TEXT NOT NULL DEFAULT '',
  target           TEXT NOT NULL,
  command_type     INTEGER NOT NULL,
  payload          TEXT NOT NULL,
  cron_expr        TEXT NOT NULL,
  risk_class       TEXT NOT NULL DEFAULT '',
  approval_id      TEXT NOT NULL DEFAULT '',
  enabled          INTEGER NOT NULL DEFAULT 1,
  last_fire_ms     INTEGER,
  next_fire_ms     INTEGER NOT NULL,
  last_command_id  TEXT NOT NULL DEFAULT '',
  created_ms       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_due ON cron_jobs(enabled, next_fire_ms);
`

// ErrNotFound 为行未找到。
var ErrNotFound = errors.New("sqlite: 行未找到")

// DB 为 SQLite 存储句柄。
type DB struct {
	sql *sql.DB
}

// Open 打开（必要时创建）数据库文件并执行嵌入式迁移。
// dsn 形如 "path/to/worktool.db"（或 ":memory:"——仅单连接时可靠，测试用
// file:…?cache=shared 更稳）。并发写靠 busy_timeout 串行化（单写者模型）。
func Open(dsn string) (*DB, error) {
	if !strings.Contains(dsn, "?") && dsn != ":memory:" {
		dsn += "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	}
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开失败: %w", err)
	}
	// modernc/sqlite 单写者：限制连接数避免 database is locked 自撞。
	sdb.SetMaxOpenConns(1)
	db := &DB{sql: sdb}
	if err := db.migrate(); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return db, nil
}

// OpenMem 打开进程内库（每调用一个独立命名库，多 goroutine 安全）。
func OpenMem() (*DB, error) {
	return Open(fmt.Sprintf("file:wtmem-%d?mode=memory&cache=shared", time.Now().UnixNano()))
}

func (db *DB) migrate() error {
	if _, err := db.sql.Exec(schema); err != nil {
		return fmt.Errorf("sqlite: 建表失败: %w", err)
	}
	var version int
	if err := db.sql.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("sqlite: 读迁移版本失败: %w", err)
	}
	if version < 1 {
		if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (1, ?)`, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("sqlite: 登记迁移失败: %w", err)
		}
	}
	// v2（T3.2）：commands 增 dry_run 列（既有库 ALTER；新库已含列）。
	if version < 2 {
		var hasCol int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('commands') WHERE name='dry_run'`).Scan(&hasCol); err != nil {
			return fmt.Errorf("sqlite: 探测 commands.dry_run 失败: %w", err)
		}
		if hasCol == 0 {
			if _, err := db.sql.Exec(`ALTER TABLE commands ADD COLUMN dry_run INTEGER NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("sqlite: 迁移 v2（commands.dry_run）失败: %w", err)
			}
		}
		if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (2, ?)`, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("sqlite: 登记迁移 v2 失败: %w", err)
		}
	}
	// v3（SR-11/G11）：allowed_file_domains 表（新库 schema 已含列；此处登记版本）。
	if version < 3 {
		if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (3, ?)`, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("sqlite: 登记迁移 v3 失败: %w", err)
		}
	}
	// v4（投递记录 http_status 独立字段车道）：outbox 增 http_status 列（回调
	// POST 响应码独立落列，与 last_error 文本区分；既有库 ALTER，新库已含列）。
	if version < 4 {
		var hasCol int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('outbox') WHERE name='http_status'`).Scan(&hasCol); err != nil {
			return fmt.Errorf("sqlite: 探测 outbox.http_status 失败: %w", err)
		}
		if hasCol == 0 {
			if _, err := db.sql.Exec(`ALTER TABLE outbox ADD COLUMN http_status INTEGER`); err != nil {
				return fmt.Errorf("sqlite: 迁移 v4（outbox.http_status）失败: %w", err)
			}
		}
		if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (4, ?)`, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("sqlite: 登记迁移 v4 失败: %w", err)
		}
	}
	return nil
}

// Close 关闭底层连接池。
func (db *DB) Close() error { return db.sql.Close() }

// Ping 验证可用性（就绪探针用）。
func (db *DB) Ping(ctx context.Context) error { return db.sql.PingContext(ctx) }

// QueryDump 执行只读查询并把全部行转为字符串矩阵（测试断言/诊断用）。
func (db *DB) QueryDump(ctx context.Context, query string, args ...any) ([][]string, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				row[i] = ""
			case []byte:
				row[i] = string(x)
			default:
				row[i] = fmt.Sprintf("%v", x)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Exec/Tx 透出最小原语供管线同事务组合。
func (db *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.sql.ExecContext(ctx, query, args...)
}

// Tx 开始事务（管线把 状态迁移+事件+审计+outbox 组进同事务，不变量②）。
func (db *DB) Tx(ctx context.Context) (*sql.Tx, error) { return db.sql.BeginTx(ctx, nil) }

// ---- 设备与配对码 ----

// UpsertDevice 注册/覆盖设备（重复 deviceId 携新配对码 = 重新绑定，spec §5.1.2）。
// robot_id 保留规则（2026-11-03 会话 9 根因修复）：Ed25519 签名公钥不变（同一物理
// 设备重配对）→ 保留既有 v1 绑定（管理面 bind 不再被配对抹掉，显式传值仍可覆盖）；
// 公钥变化（deviceId 复用/身份变更）→ 置 NULL，防旧绑定指向新凭据持有者。
func (db *DB) UpsertDevice(ctx context.Context, d Device) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO devices(device_id, robot_id, device_name, ed25519_public_key, device_x25519_public, token_hash, token_version, status, epoch, paired_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(device_id) DO UPDATE SET
  robot_id=CASE
    WHEN devices.ed25519_public_key = excluded.ed25519_public_key
      THEN COALESCE(excluded.robot_id, devices.robot_id)
    ELSE NULL
  END,
  device_name=excluded.device_name,
  ed25519_public_key=excluded.ed25519_public_key, device_x25519_public=excluded.device_x25519_public,
  token_hash=excluded.token_hash, token_version=excluded.token_version,
  status=excluded.status, epoch=MAX(devices.epoch, excluded.epoch), updated_at=excluded.updated_at`,
		d.DeviceID, nullStr(d.RobotID), d.DeviceName, d.Ed25519PublicKey, d.DeviceX25519Public, d.TokenHash, d.TokenVersion, d.Status, d.Epoch,
		d.PairedAt.UnixMilli(), time.Now().UTC().UnixMilli())
	return err
}

// GetDevice 按 deviceId 查询。
func (db *DB) GetDevice(ctx context.Context, deviceID string) (Device, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT device_id, COALESCE(robot_id,''), device_name, ed25519_public_key, device_x25519_public, token_hash, token_version, status, epoch, paired_at
FROM devices WHERE device_id = ?`, deviceID)
	var d Device
	var paired int64
	if err := row.Scan(&d.DeviceID, &d.RobotID, &d.DeviceName, &d.Ed25519PublicKey, &d.DeviceX25519Public, &d.TokenHash, &d.TokenVersion, &d.Status, &d.Epoch, &paired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, false, nil
		}
		return Device{}, false, err
	}
	d.PairedAt = time.UnixMilli(paired)
	return d, true, nil
}

// GetDeviceByEd25519 按签名公钥查设备（设备身份唯一性：同一物理设备重装/换
// 前缀后 deviceId 变化，但 Ed25519 身份公钥稳定——配对按此归并，防同机多记录）。
func (db *DB) GetDeviceByEd25519(ctx context.Context, ed25519PublicKeyHex string) (Device, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT device_id, COALESCE(robot_id,''), device_name, ed25519_public_key, device_x25519_public, token_hash, token_version, status, epoch, paired_at
FROM devices WHERE ed25519_public_key = ?`, ed25519PublicKeyHex)
	var d Device
	var paired int64
	if err := row.Scan(&d.DeviceID, &d.RobotID, &d.DeviceName, &d.Ed25519PublicKey, &d.DeviceX25519Public, &d.TokenHash, &d.TokenVersion, &d.Status, &d.Epoch, &paired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, false, nil
		}
		return Device{}, false, err
	}
	d.PairedAt = time.UnixMilli(paired)
	return d, true, nil
}

// DeleteDevice 删除设备行（管理面清理陈旧记录：同物理机旧公钥时代/误配对）。
// 关联清理：该设备 openapi-tokens 保留（robot 维度）；device_sessions 级联由
// 调用方决定（历史会话留审计）。返回是否删除（不存在=false）。
func (db *DB) DeleteDevice(ctx context.Context, deviceID string) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM devices WHERE device_id = ?`, deviceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetDeviceByRobot 按 v1 robotId 查询（v1 兼容路径桥接）。
func (db *DB) GetDeviceByRobot(ctx context.Context, robotID string) (Device, bool, error) {
	var deviceID string
	if err := db.sql.QueryRowContext(ctx, `SELECT device_id FROM devices WHERE robot_id = ?`, robotID).Scan(&deviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, false, nil
		}
		return Device{}, false, err
	}
	return db.GetDevice(ctx, deviceID)
}

// BumpEpoch 为设备分配下一个命令 epoch（单调递增，E03 效应）。
func (db *DB) BumpEpoch(ctx context.Context, deviceID string) (int64, error) {
	var epoch int64
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO devices(device_id, paired_at, updated_at) VALUES (?,?,?)
ON CONFLICT(device_id) DO NOTHING`, deviceID, time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `UPDATE devices SET epoch = epoch + 1, updated_at = ? WHERE device_id = ? RETURNING epoch`,
			time.Now().UnixMilli(), deviceID).Scan(&epoch); err != nil {
			return err
		}
		return nil
	})
	return epoch, err
}

// AllocateEpochs 为设备一次分配 n 个连续命令 epoch（性能审计 H3：原 flushQueued
// 每条指令独立 BumpEpoch 事务——一批 64 条即 64 次事务提交；此处单事务批量分配，
// 语义不变：epoch 仍单调递增、逐指令消费）。返回本批首个 epoch（first..first+n-1
// 已被本批占用；端侧拒旧水位语义允许跳号）。n<=0 不消费，返回 0。
func (db *DB) AllocateEpochs(ctx context.Context, deviceID string, n int) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	var first int64
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO devices(device_id, paired_at, updated_at) VALUES (?,?,?)
ON CONFLICT(device_id) DO NOTHING`, deviceID, time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
			return err
		}
		var last int64
		if err := tx.QueryRowContext(ctx, `UPDATE devices SET epoch = epoch + ?, updated_at = ? WHERE device_id = ? RETURNING epoch`,
			n, time.Now().UnixMilli(), deviceID).Scan(&last); err != nil {
			return err
		}
		first = last - int64(n) + 1
		return nil
	})
	return first, err
}

// EnsureEpochAtLeast 抬高设备 epoch 基线（hello.lastEpoch 拒旧语义，docs/02 §5.1：
// 服务端不得下发更小 epoch）。
func (db *DB) EnsureEpochAtLeast(ctx context.Context, deviceID string, lastEpoch int64) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO devices(device_id, epoch, paired_at, updated_at) VALUES (?,?,?,?)
ON CONFLICT(device_id) DO UPDATE SET epoch = MAX(epoch, excluded.epoch), updated_at = excluded.updated_at`,
		deviceID, lastEpoch, time.Now().UnixMilli(), time.Now().UnixMilli())
	return err
}

// CreatePairingCode 登记配对码（控制台签发；单次有效 + TTL 10min 由调用方传入）。
func (db *DB) CreatePairingCode(ctx context.Context, codeHash string, expiresAt time.Time) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO pairing_codes(code_hash, expires_at, created_at) VALUES (?,?,?)`,
		codeHash, expiresAt.UnixMilli(), time.Now().UnixMilli())
	return err
}

// ConsumePairingCode 原子消费配对码：不存在/过期/已用 → ok=false（403 PAIRING_CODE_INVALID）。
func (db *DB) ConsumePairingCode(ctx context.Context, codeHash string, now time.Time) (bool, error) {
	ok := false
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		var expires, used sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT expires_at, used_at FROM pairing_codes WHERE code_hash = ?`, codeHash).
			Scan(&expires, &used); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if !expires.Valid || expires.Int64 < now.UnixMilli() || used.Valid {
			return nil // 无效/过期/已用
		}
		res, err := tx.ExecContext(ctx, `UPDATE pairing_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`,
			now.UnixMilli(), codeHash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			ok = true
		}
		return nil
	})
	return ok, err
}

// PairingCodeRetain 过期/已用配对码保留期（L-3，安全审计 TOP5：TTL 过 24h 即删）。
const PairingCodeRetain = 24 * time.Hour

// InvalidateUnusedPairingCodes 把全部未使用旧码置 used（H-1，安全审计 TOP5：
// 签发新码时调用——单活动码语义，旧码即使未过期也不再可用，压缩爆破面）。
// 返回置 used 的行数。调用方：api.Server.CreatePairingCode / admin
// handlePairingCodes（签发路径全覆盖）。
func (db *DB) InvalidateUnusedPairingCodes(ctx context.Context, now time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE pairing_codes SET used_at = ? WHERE used_at IS NULL`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupPairingCodes 清理过期/已用配对码（L-3，安全审计 TOP5：TTL 过
// [PairingCodeRetain] 即删——expires_at 或 used_at 早于 now-24h 的行删除，
// 收敛表体积）。调用方：scheduler.RunAckTTLScanner 周期循环顺带执行。
// 返回删除行数。
func (db *DB) CleanupPairingCodes(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-PairingCodeRetain).UnixMilli()
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM pairing_codes WHERE (used_at IS NOT NULL AND used_at <= ?) OR (expires_at IS NOT NULL AND expires_at <= ?)`,
		cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 会话 ----

// CreateSession 登记会话（pair / device-sessions 两入口共用）。
func (db *DB) CreateSession(ctx context.Context, s Session) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO device_sessions(session_id, device_id, server_x25519_private, server_x25519_public, device_x25519_public, state, last_epoch, last_received_seq, encrypt_type_max, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.SessionID, s.DeviceID, s.ServerX25519Private, s.ServerX25519Public, s.DeviceX25519Public,
		"pending", s.LastEpoch, s.LastReceivedSeq, s.EncryptTypeMax, s.CreatedAt.UnixMilli())
	return err
}

// GetSession 按 sessionId 查询。
func (db *DB) GetSession(ctx context.Context, sessionID string) (Session, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT session_id, device_id, server_x25519_private, server_x25519_public, device_x25519_public, state, last_epoch, last_received_seq, encrypt_type_max, created_at
FROM device_sessions WHERE session_id = ?`, sessionID)
	var s Session
	var state string
	var created int64
	if err := row.Scan(&s.SessionID, &s.DeviceID, &s.ServerX25519Private, &s.ServerX25519Public, &s.DeviceX25519Public,
		&state, &s.LastEpoch, &s.LastReceivedSeq, &s.EncryptTypeMax, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, false, nil
		}
		return Session{}, false, err
	}
	s.State = state
	s.CreatedAt = time.UnixMilli(created)
	return s, true, nil
}

// OpenSession 标记会话为 open（hello 通过后）。
func (db *DB) OpenSession(ctx context.Context, sessionID string, lastEpoch, lastReceivedSeq, encryptTypeMax int64, openedAt time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
UPDATE device_sessions SET state='open', last_epoch=?, last_received_seq=?, encrypt_type_max=?, opened_at=? WHERE session_id=?`,
		lastEpoch, lastReceivedSeq, encryptTypeMax, openedAt.UnixMilli(), sessionID)
	return err
}

// CloseSession 标记会话关闭，并同步清零服务端会话私钥列（M-4：会话关闭
// ——含被顶替路径，gateway serveV2 defer 经本方法落库——后即不再需要解密派生，
// 私钥明文残留属敏感数据滞留；公钥保留供审计面）。
func (db *DB) CloseSession(ctx context.Context, sessionID string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE device_sessions SET state='closed', closed_at=?, server_x25519_private='' WHERE session_id=?`,
		at.UnixMilli(), sessionID)
	return err
}

// ScrubClosedSessionKeys 启动一次性清零全部已 closed 会话残留的
// server_x25519_private（M-4 兜底：历史版本关闭会话时列尚存私钥）。open/pending
// 会话保留——运行解密派生需要。返回清理行数（幂等：无残留再扫 = 0）。
func (db *DB) ScrubClosedSessionKeys(ctx context.Context) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE device_sessions SET server_x25519_private='' WHERE state='closed' AND server_x25519_private<>''`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 保留清理（服务端可观测与保留清理车道，2026-11-05；保留期 config 化，
// 2026-11-05）----
// 终态行按保留期删除（ScanQueuedTTL 同形态的周期扫描删除；调度=启动一轮 +
// 每 interval 一轮，cmd/server main.go runRetentionSweeper——interval 与各窗口
// 经 -sweeper-interval / -retention-* 旗标可配，常量仅为默认值）。

// OutboxDeliveredRetain delivered 行保留期默认值（7 天；生产窗口经
// -retention-outbox-delivered 旗标注入 CleanupOutboxRetention，保留期 config 化）。
const OutboxDeliveredRetain = 7 * 24 * time.Hour

// OutboxDeadRetain dead 行保留期默认值（30 天；生产窗口经 -retention-outbox-dead 注入）。
const OutboxDeadRetain = 30 * 24 * time.Hour

// OutboxSkippedRetain skipped 行保留期（30 天；skipped 为终态且无审计要求——
// 与 delivered/dead 同 sweeper 清理，不计入 outbox_dead_purged 审计）。
const OutboxSkippedRetain = 30 * 24 * time.Hour

// CleanupOutboxRetention 清理终态 outbox 行：delivered 超 [deliveredRetain]、
// dead 超 [deadRetain]、skipped 超 [OutboxSkippedRetain]（均按 updated_at 判龄
// ——终态迁移时刻）。dead 行删除前同事务落一条 outbox_dead_purged 审计（批量
// 计数，死信消失前留痕）；skipped 清理无审计要求；pending 行不动。
// 返回 (delivered, dead, skipped) 删除行数（幂等：再扫 = 0,0,0）。
func (db *DB) CleanupOutboxRetention(ctx context.Context, now time.Time, deliveredRetain, deadRetain time.Duration) (delivered, dead, skipped int64, err error) {
	err = db.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM outbox WHERE status='dead' AND updated_at <= ?`,
			now.Add(-deadRetain).UnixMilli()).Scan(&dead); err != nil {
			return err
		}
		if dead > 0 {
			// 清理前审计（与删除同事务：崩溃间隙不留无痕删除）。
			if err := db.InsertAudit(ctx, tx, "outbox_dead_purged", "",
				fmt.Sprintf("count=%d（dead 行保留期 %s，按 updated_at 判龄）", dead, deadRetain), now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM outbox WHERE status='dead' AND updated_at <= ?`,
				now.Add(-deadRetain).UnixMilli()); err != nil {
				return err
			}
		}
		// skipped（W-M6 无接收器终态）与 delivered/dead 同 sweeper 清理；
		// 无审计要求——批量删除即可。
		resSkip, err := tx.ExecContext(ctx,
			`DELETE FROM outbox WHERE status='skipped' AND updated_at <= ?`,
			now.Add(-OutboxSkippedRetain).UnixMilli())
		if err != nil {
			return err
		}
		skipped, _ = resSkip.RowsAffected()
		res, err := tx.ExecContext(ctx,
			`DELETE FROM outbox WHERE status='delivered' AND updated_at <= ?`,
			now.Add(-deliveredRetain).UnixMilli())
		if err != nil {
			return err
		}
		delivered, _ = res.RowsAffected()
		return nil
	})
	return delivered, dead, skipped, err
}

// ClosedSessionRetain closed 会话整行保留期默认值（30 天；server_x25519_private
// 关闭即清零语义已存在（M-4），到期后连元数据一并整行删除；生产窗口经
// -retention-session-closed 旗标注入 CleanupClosedSessions）。
const ClosedSessionRetain = 30 * 24 * time.Hour

// CleanupClosedSessions 清理超期 closed 会话整行（closed_at 超 [retain]；
// closed_at 缺失的历史行按 created_at 兜底判龄）。open/pending 行不动（运行需要）。
// 返回删除行数（幂等：再扫 = 0）。
func (db *DB) CleanupClosedSessions(ctx context.Context, now time.Time, retain time.Duration) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM device_sessions WHERE state='closed' AND COALESCE(closed_at, created_at) <= ?`,
		now.Add(-retain).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 指令 / 事件 / 幂等 / 墓碑 / outbox / 审计 / 入向去重 ----

// InsertCommand 新建指令（E01 enqueue 或 E02 reject 前的载体落库）。
func (db *DB) InsertCommand(ctx context.Context, tx *sql.Tx, c Command) error {
	exec := db.exec(ctx, tx)
	_, err := exec(`
INSERT INTO commands(command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.CommandID, c.Target, c.CommandType, c.Payload, c.State, c.Epoch, c.Seq, c.EventID, c.RiskClass, boolInt(c.DryRun), c.CreatedAt.UnixMilli())
	return err
}

// UpdateCommandState 更新指令状态（迁移落库）。
func (db *DB) UpdateCommandState(ctx context.Context, tx *sql.Tx, c Command) error {
	exec := db.exec(ctx, tx)
	_, err := exec(`UPDATE commands SET state=?, epoch=?, seq=?, event_id=?, dry_run=?, dispatched_at=?, ack_deadline=?, terminal_at=?, terminal_state=? WHERE command_id=?`,
		c.State, c.Epoch, c.Seq, c.EventID, boolInt(c.DryRun),
		nullMilli(c.DispatchedAt), nullMilli(c.AckDeadline), nullMilli(c.TerminalAt), nullStrPtr(c.TerminalState),
		c.CommandID)
	return err
}

// GetCommand 按 commandID 查询。
func (db *DB) GetCommand(ctx context.Context, commandID string) (Command, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at, dispatched_at, ack_deadline, terminal_at, terminal_state
FROM commands WHERE command_id = ?`, commandID)
	return scanCommand(row)
}

// GetCommandForUpdate 在事务内取指令行（SQLite 整库锁下仍按约定加 IMMEDIATE 语义由调用方保证）。
func (db *DB) GetCommandForUpdate(ctx context.Context, tx *sql.Tx, commandID string) (Command, bool, error) {
	row := tx.QueryRowContext(ctx, `
SELECT command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at, dispatched_at, ack_deadline, terminal_at, terminal_state
FROM commands WHERE command_id = ?`, commandID)
	return scanCommand(row)
}

func scanCommand(row interface{ Scan(...any) error }) (Command, bool, error) {
	var c Command
	var created, dryRun int64
	var dispatched, deadline, terminal sql.NullInt64
	var terminalState sql.NullString
	if err := row.Scan(&c.CommandID, &c.Target, &c.CommandType, &c.Payload, &c.State, &c.Epoch, &c.Seq, &c.EventID,
		&c.RiskClass, &dryRun, &created, &dispatched, &deadline, &terminal, &terminalState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Command{}, false, nil
		}
		return Command{}, false, err
	}
	c.DryRun = dryRun != 0
	c.CreatedAt = time.UnixMilli(created)
	c.DispatchedAt = milliTime(dispatched)
	c.AckDeadline = milliTime(deadline)
	c.TerminalAt = milliTime(terminal)
	if terminalState.Valid {
		c.TerminalState = sql.NullString{String: terminalState.String, Valid: true}
	}
	return c, true, nil
}

// QueuedForTarget 返回目标的待下发指令（queued，FIFO）。
// dry_run 指令除外（FR-54：干跑不产生真实副作用，绝不下发设备——由
// ExecuteDryRun 在服务端同步走完同一状态机）。
func (db *DB) QueuedForTarget(ctx context.Context, target string, limit int) ([]Command, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at, dispatched_at, ack_deadline, terminal_at, terminal_state
FROM commands WHERE target = ? AND state = 'queued' AND dry_run = 0 ORDER BY created_at LIMIT ?`, target, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCommands(rows)
}

// InflightStates 返回处于执行链路上的指令（dispatched/acked/running）。
func (db *DB) InflightStates(ctx context.Context) (map[string][]Command, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at, dispatched_at, ack_deadline, terminal_at, terminal_state
FROM commands WHERE state IN ('dispatched','acked','running')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cmds, err := collectCommands(rows)
	if err != nil {
		return nil, err
	}
	byState := map[string][]Command{}
	for _, c := range cmds {
		byState[c.State] = append(byState[c.State], c)
	}
	return byState, nil
}

// DueAckTTL 返回 ACK TTL 到期的 dispatched/acked 指令（E17/E18 扫描输入）。
func (db *DB) DueAckTTL(ctx context.Context, now time.Time) ([]Command, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at, dispatched_at, ack_deadline, terminal_at, terminal_state
FROM commands WHERE state IN ('dispatched','acked') AND ack_deadline IS NOT NULL AND ack_deadline < ?`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCommands(rows)
}

// PendingUnknownRisk 返回 pending 的 execution_unknown 指令风险类集合（FR-53 冻结判定）。
func (db *DB) PendingUnknownRisk(ctx context.Context) (map[string]int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT risk_class, COUNT(*) FROM commands WHERE state='execution_unknown' GROUP BY risk_class`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var rc string
		var n int
		if err := rows.Scan(&rc, &n); err != nil {
			return nil, err
		}
		out[rc] = n
	}
	return out, rows.Err()
}

// CommandsByState 返回指定状态的指令（测试/对账）。
func (db *DB) CommandsByState(ctx context.Context, state string) ([]Command, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at, dispatched_at, ack_deadline, terminal_at, terminal_state
FROM commands WHERE state = ? ORDER BY created_at`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCommands(rows)
}

func collectCommands(rows *sql.Rows) ([]Command, error) {
	var out []Command
	for rows.Next() {
		c, ok, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// InsertEvent 留痕一条边（不变量①）。
func (db *DB) InsertEvent(ctx context.Context, tx *sql.Tx, e CommandEvent) error {
	exec := db.exec(ctx, tx)
	_, err := exec(`INSERT INTO command_events(command_id, edge_id, from_state, to_state, event, detail, at) VALUES (?,?,?,?,?,?,?)`,
		e.CommandID, e.EdgeID, e.FromState, e.ToState, e.Event, e.Detail, e.At.UnixMilli())
	return err
}

// Events 返回指令事件流（对账/测试）。
func (db *DB) Events(ctx context.Context, commandID string) ([]CommandEvent, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, command_id, edge_id, from_state, to_state, event, detail, at FROM command_events WHERE command_id=? ORDER BY id`, commandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandEvent
	for rows.Next() {
		var e CommandEvent
		var at int64
		if err := rows.Scan(&e.ID, &e.CommandID, &e.EdgeID, &e.FromState, &e.ToState, &e.Event, &e.Detail, &at); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// HasEventEdge 报告指令是否已有指定边 ID 的留痕（TTL 证据判定：
// dispatched→E17 看 transport_ack 留痕，acked→E18 看 exec_start 留痕）。
func (db *DB) HasEventEdge(ctx context.Context, tx *sql.Tx, commandID, edgeID string) (bool, error) {
	query := db.query(ctx, tx)
	var n int
	if err := query(`SELECT COUNT(*) FROM command_events WHERE command_id=? AND edge_id=?`, commandID, edgeID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// InsertAudit 审计留痕（同事务接口）。
func (db *DB) InsertAudit(ctx context.Context, tx *sql.Tx, kind, subject, detail string, at time.Time) error {
	exec := db.exec(ctx, tx)
	_, err := exec(`INSERT INTO audit_log(kind, subject, detail, at) VALUES (?,?,?,?)`, kind, subject, detail, at.UnixMilli())
	return err
}

// IdempotencyGet 查询幂等键（同键同参重放返回原响应）。
// tx 可为 nil（直连）；事务内调用必须传 tx——单连接池下直连会等待事务释放造成死锁。
func (db *DB) IdempotencyGet(ctx context.Context, tx *sql.Tx, key string) (paramsHash, commandID, response string, createdAt time.Time, ok bool, err error) {
	var created int64
	err = db.query(ctx, tx)(`SELECT params_hash, command_id, response, created_at FROM idempotency_keys WHERE key=?`, key).
		Scan(&paramsHash, &commandID, &response, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", time.Time{}, false, nil
		}
		return "", "", "", time.Time{}, false, err
	}
	return paramsHash, commandID, response, time.UnixMilli(created), true, nil
}

// IdempotencyPut 落库幂等键（同事务）。
func (db *DB) IdempotencyPut(ctx context.Context, tx *sql.Tx, key, paramsHash, commandID, response string, at time.Time) error {
	_, err := db.exec(ctx, tx)(`INSERT OR REPLACE INTO idempotency_keys(key, params_hash, command_id, response, created_at) VALUES (?,?,?,?,?)`,
		key, paramsHash, commandID, response, at.UnixMilli())
	return err
}

// InsertTombstone 写设备墓碑（仅元数据；终态时与迁移同事务）。
func (db *DB) InsertTombstone(ctx context.Context, tx *sql.Tx, tb Tombstone) error {
	_, err := db.exec(ctx, tx)(`INSERT OR REPLACE INTO tombstones(command_id, event_id, final_state, terminal_at, purge_after) VALUES (?,?,?,?,?)`,
		tb.CommandID, tb.EventID, tb.FinalState, tb.TerminalAt.UnixMilli(), tb.PurgeAfter.UnixMilli())
	return err
}

// PurgeDueTombstones 清理到期墓碑（guard：该 commandId 无 pending execution_unknown）。
func (db *DB) PurgeDueTombstones(ctx context.Context, now time.Time) (int, error) {
	res, err := db.sql.ExecContext(ctx, `
DELETE FROM tombstones WHERE purge_after <= ?
  AND command_id NOT IN (SELECT command_id FROM commands WHERE state='execution_unknown')`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Tombstones 返回全部墓碑（测试）。
func (db *DB) Tombstones(ctx context.Context) ([]Tombstone, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT command_id, event_id, final_state, terminal_at, purge_after FROM tombstones ORDER BY terminal_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tombstone
	for rows.Next() {
		var tb Tombstone
		var ta, pa int64
		if err := rows.Scan(&tb.CommandID, &tb.EventID, &tb.FinalState, &ta, &pa); err != nil {
			return nil, err
		}
		tb.TerminalAt = time.UnixMilli(ta)
		tb.PurgeAfter = time.UnixMilli(pa)
		out = append(out, tb)
	}
	return out, rows.Err()
}

// EnqueueOutbox 入队回调出站（与结果同事务，不变量②）。
func (db *DB) EnqueueOutbox(ctx context.Context, tx *sql.Tx, kind, commandID string, payload any, at time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.exec(ctx, tx)(`INSERT INTO outbox(command_id, kind, payload, attempts, next_attempt, status, created_at, updated_at)
VALUES (?,?,?,0,?,'pending',?,?)`, commandID, kind, string(raw), at.UnixMilli(), at.UnixMilli(), at.UnixMilli())
	return err
}

// DueOutbox 取到期待投递出站行（claim：仅 pending 且 next_attempt <= now）。
// **部署约束（补充审计 2026-11-04）：非原子领取（普通 SELECT，无 FOR UPDATE/
// 租约）——单进程单 worker 前提**（main.go 恰好单 goroutine Run）。多实例部署需
// 先行改造：原子领取（UPDATE ... WHERE status='pending' 抢占）+ lease/fencing
// （过期 worker 写入拒绝）——归 PG 存储车道（docs/11 §D T4.3）一并设计。
func (db *DB) DueOutbox(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, command_id, kind, payload, attempts, next_attempt, status, last_error, created_at, updated_at, http_status
FROM outbox WHERE status='pending' AND next_attempt <= ? ORDER BY id LIMIT ?`, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOutbox(rows)
}

// UpdateOutbox 更新出站行状态（含最近投递 HTTP 响应码 http_status；无 HTTP
// 响应传 sql.NullInt64{} 落 NULL）。
func (db *DB) UpdateOutbox(ctx context.Context, it OutboxItem) error {
	_, err := db.sql.ExecContext(ctx, `
UPDATE outbox SET attempts=?, next_attempt=?, status=?, last_error=?, updated_at=?, http_status=? WHERE id=?`,
		it.Attempts, it.NextAttempt.UnixMilli(), it.Status, it.LastError, time.Now().UTC().UnixMilli(), nullInt(it.HTTPStatus), it.ID)
	return err
}

// OutboxByStatus 按状态列出出站行（测试）。
func (db *DB) OutboxByStatus(ctx context.Context, status string) ([]OutboxItem, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, command_id, kind, payload, attempts, next_attempt, status, last_error, created_at, updated_at, http_status
FROM outbox WHERE status=? ORDER BY id`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOutbox(rows)
}

func collectOutbox(rows *sql.Rows) ([]OutboxItem, error) {
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var na, ca, ua int64
		if err := rows.Scan(&it.ID, &it.CommandID, &it.Kind, &it.Payload, &it.Attempts, &na, &it.Status, &it.LastError, &ca, &ua, &it.HTTPStatus); err != nil {
			return nil, err
		}
		it.NextAttempt = time.UnixMilli(na)
		it.CreatedAt = time.UnixMilli(ca)
		it.UpdatedAt = time.UnixMilli(ua)
		out = append(out, it)
	}
	return out, rows.Err()
}

// RememberInboundEvent 记录入向帧（eventId 去重）。已存在时：
// 同 digest → 幂等命中（dup=true, conflict=false）；异 digest → 冲突（4405）。
func (db *DB) RememberInboundEvent(ctx context.Context, deviceID, eventID, digest, receipt string, at time.Time) (dup, conflict bool, err error) {
	err = db.withTx(ctx, func(tx *sql.Tx) error {
		var haveDigest string
		err := tx.QueryRowContext(ctx, `SELECT payload_digest FROM inbound_events WHERE device_id=? AND event_id=?`, deviceID, eventID).Scan(&haveDigest)
		switch {
		case err == nil:
			dup = true
			conflict = haveDigest != digest
			return nil
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO inbound_events(device_id, event_id, payload_digest, receipt, at) VALUES (?,?,?,?,?)`,
				deviceID, eventID, digest, receipt, at.UnixMilli())
			return err
		default:
			return err
		}
	})
	return dup, conflict, err
}

// GetInboundReceipt 返回历史回执（幂等回执原确认用）。
func (db *DB) GetInboundReceipt(ctx context.Context, deviceID, eventID string) (string, bool, error) {
	var receipt string
	err := db.sql.QueryRowContext(ctx, `SELECT receipt FROM inbound_events WHERE device_id=? AND event_id=?`, deviceID, eventID).Scan(&receipt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return receipt, true, nil
}

// SetInboundReceipt 回填已记忆入向帧的回执（性能审计 H1 服务端半边：回执在
// 处理完成后才产生，去重记忆先于处理——receipt 事后回填同键行）。幂等：重放
// 处理不覆盖既有回执（空 receipt 才写入，防并发乱序回退）。行不存在 = no-op
// （帧未被记忆即未被消费，无回执语义）。
func (db *DB) SetInboundReceipt(ctx context.Context, deviceID, eventID, receipt string) error {
	if receipt == "" {
		return nil
	}
	_, err := db.sql.ExecContext(ctx, `
UPDATE inbound_events SET receipt=? WHERE device_id=? AND event_id=? AND receipt=''`, receipt, deviceID, eventID)
	return err
}

// ---- T3.2：审批（FR-53 L2）/ 白名单（L1）/ API 令牌（FR-34）/ cron（FR-38）----

// InsertApproval 落一条审批请求（pending）。
func (db *DB) InsertApproval(ctx context.Context, a Approval) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO approvals(approval_id, tenant, actor, target, command_type, params_digest, risk_class, status, approver, reason, recurring, created_ms, decided_ms)
VALUES (?,?,?,?,?,?,?,'pending','',?,?,?,NULL)`,
		a.ApprovalID, a.Tenant, a.Actor, a.Target, a.CommandType, a.ParamsDigest, a.RiskClass, a.Reason, boolInt(a.Recurring), a.CreatedAt.UnixMilli())
	return err
}

// GetApproval 按 approvalID 查询。
func (db *DB) GetApproval(ctx context.Context, approvalID string) (Approval, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT approval_id, tenant, actor, target, command_type, params_digest, risk_class, status, approver, reason, recurring, created_ms, decided_ms
FROM approvals WHERE approval_id = ?`, approvalID)
	var a Approval
	var recurring int
	var created int64
	var decided sql.NullInt64
	if err := row.Scan(&a.ApprovalID, &a.Tenant, &a.Actor, &a.Target, &a.CommandType, &a.ParamsDigest, &a.RiskClass,
		&a.Status, &a.Approver, &a.Reason, &recurring, &created, &decided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Approval{}, false, nil
		}
		return Approval{}, false, err
	}
	a.Recurring = recurring != 0
	a.CreatedAt = time.UnixMilli(created)
	a.DecidedAt = milliTime(decided)
	return a, true, nil
}

// DecideApproval 事务内决议审批：仅 pending 可决议（幂等冲突返回 ok=false）。
func (db *DB) DecideApproval(ctx context.Context, approvalID, status, approver, reason string, decidedAt time.Time) (bool, error) {
	ok := false
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
UPDATE approvals SET status=?, approver=?, reason=?, decided_ms=? WHERE approval_id=? AND status='pending'`,
			status, approver, reason, decidedAt.UnixMilli(), approvalID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n == 1
		return nil
	})
	return ok, err
}

// ConsumeApprovalTx 事务内把一次性审批标记为 consumed（approved → consumed；
// 常设审批不动）。与 E01 入队同事务（CR-t32-T3.2-R3）：入队失败整体回滚不烧审批。
// 返回是否生效（0 行 = 已被并发消费/非常设审批态，调用方 fail-closed）。
func (db *DB) ConsumeApprovalTx(ctx context.Context, tx *sql.Tx, approvalID string, at time.Time) (bool, error) {
	res, err := db.exec(ctx, tx)(`
UPDATE approvals SET status='consumed', decided_ms=COALESCE(decided_ms, ?) WHERE approval_id=? AND status='approved' AND recurring=0`,
		at.UnixMilli(), approvalID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ApprovalsByStatus 按状态列出审批（测试/审计面）。
func (db *DB) ApprovalsByStatus(ctx context.Context, status string) ([]Approval, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT approval_id, tenant, actor, target, command_type, params_digest, risk_class, status, approver, reason, recurring, created_ms, decided_ms
FROM approvals WHERE status=? ORDER BY created_ms`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		var a Approval
		var recurring int
		var created int64
		var decided sql.NullInt64
		if err := rows.Scan(&a.ApprovalID, &a.Tenant, &a.Actor, &a.Target, &a.CommandType, &a.ParamsDigest, &a.RiskClass,
			&a.Status, &a.Approver, &a.Reason, &recurring, &created, &decided); err != nil {
			return nil, err
		}
		a.Recurring = recurring != 0
		a.CreatedAt = time.UnixMilli(created)
		a.DecidedAt = milliTime(decided)
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddRecipientWhitelist 登记接收人白名单（upsert）。
func (db *DB) AddRecipientWhitelist(ctx context.Context, target, recipient, addedBy string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO recipient_whitelist(target, recipient, added_by, at_ms) VALUES (?,?,?,?)
ON CONFLICT(target, recipient) DO UPDATE SET added_by=excluded.added_by, at_ms=excluded.at_ms`,
		target, recipient, addedBy, at.UnixMilli())
	return err
}

// RemoveRecipientWhitelist 移除一条白名单。
func (db *DB) RemoveRecipientWhitelist(ctx context.Context, target, recipient string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM recipient_whitelist WHERE target=? AND recipient=?`, target, recipient)
	return err
}

// WhitelistForTarget 返回目标的全部白名单接收人（空切片 = 未配置白名单）。
func (db *DB) WhitelistForTarget(ctx context.Context, target string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT recipient FROM recipient_whitelist WHERE target=? ORDER BY at_ms`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertAPIToken 登记兼容面令牌（仅 hash）。
func (db *DB) InsertAPIToken(ctx context.Context, tokenHash, robotID, tenant string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO api_tokens(token_hash, robot_id, tenant, status, created_ms) VALUES (?,?,?,'active',?)`,
		tokenHash, robotID, tenant, at.UnixMilli())
	return err
}

// AddAllowedFileDomain 登记外链资源域白名单（SR-11；upsert）。
func (db *DB) AddAllowedFileDomain(ctx context.Context, domain, addedBy string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO allowed_file_domains(domain, added_by, at_ms) VALUES (?,?,?)
ON CONFLICT(domain) DO UPDATE SET added_by=excluded.added_by, at_ms=excluded.at_ms`,
		domain, addedBy, at.UnixMilli())
	return err
}

// AllowedFileDomains 返回全部登记的文件域名（空切片 = 未登记任何域 → 任何 fileUrl 拒收）。
func (db *DB) AllowedFileDomains(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT domain FROM allowed_file_domains ORDER BY at_ms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetAPIToken 按 hash 查询令牌（原始 token 不落库——按 hash 索引查询即比对）。
func (db *DB) GetAPIToken(ctx context.Context, tokenHash string) (APIToken, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT token_hash, robot_id, tenant, status, created_ms, last_used_ms FROM api_tokens WHERE token_hash=?`, tokenHash)
	var t APIToken
	var created int64
	var lastUsed sql.NullInt64
	if err := row.Scan(&t.TokenHash, &t.RobotID, &t.Tenant, &t.Status, &created, &lastUsed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIToken{}, false, nil
		}
		return APIToken{}, false, err
	}
	t.CreatedAt = time.UnixMilli(created)
	t.LastUsedAt = milliTime(lastUsed)
	return t, true, nil
}

// TouchAPIToken 记录令牌最近使用（审计辅助；失败不阻断请求）。
func (db *DB) TouchAPIToken(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE api_tokens SET last_used_ms=? WHERE token_hash=?`, at.UnixMilli(), tokenHash)
	return err
}

// InsertCronJob 落一条 cron 任务。
func (db *DB) InsertCronJob(ctx context.Context, j CronJob) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO cron_jobs(job_id, name, tenant, actor, target, command_type, payload, cron_expr, risk_class, approval_id, enabled, last_fire_ms, next_fire_ms, last_command_id, created_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.JobID, j.Name, j.Tenant, j.Actor, j.Target, j.CommandType, j.Payload, j.CronExpr, j.RiskClass, j.ApprovalID,
		boolInt(j.Enabled), nullMilli(j.LastFireAt), j.NextFireAt.UnixMilli(), j.LastCommandID, j.CreatedAt.UnixMilli())
	return err
}

// DueCronJobs 返回到期任务（enabled 且 next_fire <= now）。
func (db *DB) DueCronJobs(ctx context.Context, now time.Time, limit int) ([]CronJob, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT job_id, name, tenant, actor, target, command_type, payload, cron_expr, risk_class, approval_id, enabled, last_fire_ms, next_fire_ms, last_command_id, created_ms
FROM cron_jobs WHERE enabled=1 AND next_fire_ms <= ? ORDER BY next_fire_ms LIMIT ?`, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCronJobs(rows)
}

// UpdateCronJobFire 登记一次触发（last_fire/next_fire/last_command_id）。
func (db *DB) UpdateCronJobFire(ctx context.Context, jobID, lastCommandID string, lastFire, nextFire time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
UPDATE cron_jobs SET last_fire_ms=?, next_fire_ms=?, last_command_id=? WHERE job_id=?`,
		lastFire.UnixMilli(), nextFire.UnixMilli(), lastCommandID, jobID)
	return err
}

// SetCronJobEnabled 启停任务。
func (db *DB) SetCronJobEnabled(ctx context.Context, jobID string, enabled bool) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE cron_jobs SET enabled=? WHERE job_id=?`, boolInt(enabled), jobID)
	return err
}

// CronJobs 列出全部任务（测试/运维面）。
func (db *DB) CronJobs(ctx context.Context) ([]CronJob, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT job_id, name, tenant, actor, target, command_type, payload, cron_expr, risk_class, approval_id, enabled, last_fire_ms, next_fire_ms, last_command_id, created_ms
FROM cron_jobs ORDER BY created_ms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectCronJobs(rows)
}

// GetCronJob 取单条任务（管理面启停/删除前置校验）。
func (db *DB) GetCronJob(ctx context.Context, jobID string) (CronJob, bool, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT job_id, name, tenant, actor, target, command_type, payload, cron_expr, risk_class, approval_id, enabled, last_fire_ms, next_fire_ms, last_command_id, created_ms
FROM cron_jobs WHERE job_id = ?`, jobID)
	if err != nil {
		return CronJob{}, false, err
	}
	defer rows.Close()
	out, err := collectCronJobs(rows)
	if err != nil || len(out) != 1 {
		return CronJob{}, false, err
	}
	return out[0], true, nil
}

// DeleteCronJob 删除任务行（管理面；历史触发的指令行保留——审计不可毁）。
// 返回是否确有删除。
func (db *DB) DeleteCronJob(ctx context.Context, jobID string) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM cron_jobs WHERE job_id = ?`, jobID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func collectCronJobs(rows *sql.Rows) ([]CronJob, error) {
	var out []CronJob
	for rows.Next() {
		var j CronJob
		var enabled int
		var lastFire sql.NullInt64
		var nextFire, created int64
		if err := rows.Scan(&j.JobID, &j.Name, &j.Tenant, &j.Actor, &j.Target, &j.CommandType, &j.Payload, &j.CronExpr,
			&j.RiskClass, &j.ApprovalID, &enabled, &lastFire, &nextFire, &j.LastCommandID, &created); err != nil {
			return nil, err
		}
		j.Enabled = enabled != 0
		j.LastFireAt = milliTime(lastFire)
		j.NextFireAt = time.UnixMilli(nextFire)
		j.CreatedAt = time.UnixMilli(created)
		out = append(out, j)
	}
	return out, rows.Err()
}

// OutboxByCommand 返回该指令的全部出站行（RAW_CONFIRM 结果查询取 kind=exec_result）。
func (db *DB) OutboxByCommand(ctx context.Context, commandID string) ([]OutboxItem, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, command_id, kind, payload, attempts, next_attempt, status, last_error, created_at, updated_at, http_status
FROM outbox WHERE command_id=? ORDER BY id`, commandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOutbox(rows)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- 内部辅助 ----

func (db *DB) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// exec 返回事务/直连自适应的执行器。
func (db *DB) exec(ctx context.Context, tx *sql.Tx) func(string, ...any) (sql.Result, error) {
	if tx != nil {
		return func(q string, args ...any) (sql.Result, error) { return tx.ExecContext(ctx, q, args...) }
	}
	return func(q string, args ...any) (sql.Result, error) { return db.sql.ExecContext(ctx, q, args...) }
}

// query 返回事务/直连自适应的单行查询器。
func (db *DB) query(ctx context.Context, tx *sql.Tx) func(string, ...any) *sql.Row {
	if tx != nil {
		return func(q string, args ...any) *sql.Row { return tx.QueryRowContext(ctx, q, args...) }
	}
	return func(q string, args ...any) *sql.Row { return db.sql.QueryRowContext(ctx, q, args...) }
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullStrPtr(ns sql.NullString) any {
	if !ns.Valid {
		return nil
	}
	return ns.String
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

func nullMilli(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UnixMilli()
}

func milliTime(n sql.NullInt64) sql.NullTime {
	if !n.Valid {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: time.UnixMilli(n.Int64), Valid: true}
}
