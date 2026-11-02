// apifox 兼容 REST 适配层（FR-34，ADR-08；任务卡 T3.2）。
//
// 以旧面语义（robot_id 寻址、指令报文、RAW_CONFIRM 结果查询、60QPM 限流，
// docs/01 FR-34 + NFR §2.2 + local-private/audit/02 §A 指令表）封装到统一命令
// 管线；不引入旧协议缺陷——明文/无鉴权不做，兼容层同样要求 token
// （Authorization: Bearer，仅存 SHA-256 hash，spec §5.1.3 同纪律）。
//
// 指令映射（WTO-004 防裸数字混淆）：v1 数字指令 → 内部 commandIntent 采用
// 命名空间隔离的正式映射表（openapi.v1.command.<name>），映射放 server 侧；
// 注册表之外的一切 type（含上报类 11/101/102/301/302 与未定义类型）一律
// UNKNOWN_COMMAND 拒绝，绝不把裸数字常量直接当 operation。
package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// openapiIntent 为映射表条目：v1 数字指令 → 命名空间隔离的内部 commandIntent。
type openapiIntent struct {
	Type int
	Name string // intent 短名（全名 = "openapi.v1.command." + Name）
}

// openapiCommandRegistry 为兼容面指令注册表（云→端指令 201~234/303/304/501~509，
// 名称对齐 spec §4.2 指令表）。上报/心跳类（11/101/102/301/302）与上游未定义
// 类型（215/216/217/231/232）刻意不注册 → 提交即 UNKNOWN_COMMAND（WTO-004
// 碰撞拒绝：裸数字不得映射为 operation）。
var openapiCommandRegistry = []openapiIntent{
	{201, "stop_and_go_home"}, {202, "loop_receive_new_message"}, {203, "send_message"},
	{204, "reply_message"}, {205, "relay_message"}, {206, "init_group"},
	{207, "update_group"}, {208, "push_micro_disk_image"}, {209, "push_micro_disk_file"},
	{210, "push_microprogram"}, {211, "push_office"}, {213, "add_friend_by_phone"},
	{214, "show_group_info"}, {218, "push_file"}, {219, "dismiss_group"},
	{220, "add_friend_by_group"}, {221, "add_need_deal"}, {222, "clock_in"},
	{223, "switch_corp"}, {224, "push_link"}, {225, "modify_group_member_info"},
	{226, "recall_message"}, {227, "relay_multi_message"}, {228, "relay_merge_message"},
	{229, "send_multi_message"}, {230, "send_merge_message"}, {233, "scan_qr_code"},
	{234, "delete_contact"},
	{303, "robot_controller_test"}, {304, "robot_queue_clear"},
	{501, "get_group_info"}, {502, "get_friend_info"}, {503, "get_my_info"},
	{504, "get_group_qrcode"}, {505, "get_recent_list"}, {506, "get_corp_list"},
	{507, "get_all_friend_info"}, {508, "get_all_group_info"}, {509, "get_local_file"},
}

// openapiIntentByType 建 type → intent 反查表（构造期一次）。
var openapiIntentByType = func() map[int]string {
	m := make(map[int]string, len(openapiCommandRegistry))
	for _, it := range openapiCommandRegistry {
		m[it.Type] = "openapi.v1.command." + it.Name
	}
	return m
}()

// OpenAPIIntent 返回数字指令的命名空间隔离 intent（未注册 type 返回空）。
func OpenAPIIntent(commandType int) string { return openapiIntentByType[commandType] }

// openapi 错误码（服务端分配段；已知上游值 0/101012/201011/201108/501011/501012/501013
// 不复用不覆盖，spec §4.3/§9 Q2 完整表留 W-M4 apifox 全量冒烟冻结）。
const (
	openapiCodeSuccess          = 0
	openapiCodeInvalidBody      = 101001
	openapiCodeUnknownCommand   = 101002
	openapiCodeIdemKeyRequired  = 101003
	openapiCodeRateLimited      = 101004
	openapiCodeNotWhitelisted   = 101005
	openapiCodeApprovalRequired = 101006
	openapiCodeIdemConflict     = 101007
	openapiCodeRiskFrozen       = 101008
	openapiCodeCircuitOpen      = 101009
	openapiCodeNotFound         = 101010
	openapiCodeInvalidToken     = 501012 // 对齐上游"robotKey 错"语义（token 即兼容面凭据）
	openapiCodeRobotMismatch    = 501011 // 对齐上游"robotId 错"语义
)

// OpenAPIConfig 为兼容面配置（默认 60QPM，NFR §2.2 对齐 apifox 限制值）。
type OpenAPIConfig struct {
	RateLimitPerMinute int           // 默认 60
	RateWindow         time.Duration // 默认 60s
}

func (c OpenAPIConfig) withDefaults() OpenAPIConfig {
	if c.RateLimitPerMinute <= 0 {
		c.RateLimitPerMinute = 60
	}
	if c.RateWindow <= 0 {
		c.RateWindow = time.Minute
	}
	return c
}

// openapiLimiter 为按 robot 的滚动窗口限流器（60QPM 默认）。
type openapiLimiter struct {
	mu   sync.Mutex
	cfg  OpenAPIConfig
	hits map[string][]time.Time
}

func newOpenAPILimiter(cfg OpenAPIConfig) *openapiLimiter {
	return &openapiLimiter{cfg: cfg.withDefaults(), hits: map[string][]time.Time{}}
}

func (l *openapiLimiter) allow(robotID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.cfg.RateWindow)
	kept := l.hits[robotID][:0]
	for _, t := range l.hits[robotID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.hits[robotID] = kept
	if len(kept) >= l.cfg.RateLimitPerMinute {
		return false
	}
	l.hits[robotID] = append(l.hits[robotID], now)
	return true
}

// ---- API 令牌（兼容面鉴权；仅存 hash） ----

// IssueOpenAPIToken 为 robot 签发兼容面令牌（原始值仅此一次返回；admin 面随
// T3.x RBAC 落地，本方法供运维联调与测试消费）。
func (s *Server) IssueOpenAPIToken(ctx context.Context, robotID, tenant string) (string, error) {
	if tenant == "" {
		tenant = "default"
	}
	raw, err := wire.RandomHex(32)
	if err != nil {
		return "", err
	}
	if err := s.DB.InsertAPIToken(ctx, sha256Hex(raw), robotID, tenant, s.Now().UTC()); err != nil {
		return "", err
	}
	s.Log.Printf("api/openapi: 签发兼容面令牌 robot=%s tenant=%s（仅存 hash）", robotID, tenant)
	return raw, nil
}

// RevokeOpenAPIToken 吊销令牌。
func (s *Server) RevokeOpenAPIToken(ctx context.Context, rawToken string) error {
	// 状态置 revoked：GetOpenAPIToken 校验 status
	_, err := s.DB.Exec(ctx, `UPDATE api_tokens SET status='revoked' WHERE token_hash=?`, sha256Hex(rawToken))
	return err
}

// authenticate 校验 Bearer token 并核对 robotId 寻址一致（旧面 robotId 即身份
// 的缺陷在此修复：token 与路径 robotId 须同时匹配）。成功返回令牌行（含租户）。
func (s *Server) authenticate(ctx context.Context, r *http.Request, robotID string) (sqlite.APIToken, *apiError) {
	auth := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		return sqlite.APIToken{}, &apiError{http.StatusUnauthorized, "INVALID_TOKEN", "缺少 Authorization: Bearer <token>（兼容层强制鉴权，不沿用旧面无鉴权缺陷）"}
	}
	t, found, err := s.DB.GetAPIToken(ctx, sha256Hex(raw))
	if err != nil {
		return sqlite.APIToken{}, &apiError{http.StatusInternalServerError, "STORE", err.Error()}
	}
	if !found || t.Status != "active" {
		return sqlite.APIToken{}, &apiError{http.StatusUnauthorized, "INVALID_TOKEN", "token 无效或已吊销（对齐上游 501012 robotKey 语义）"}
	}
	if t.RobotID != robotID {
		return sqlite.APIToken{}, &apiError{http.StatusForbidden, "ROBOT_MISMATCH", fmt.Sprintf("token 不属于 robot %s（对齐上游 501011 robotId 语义）", robotID)}
	}
	_ = s.DB.TouchAPIToken(ctx, t.TokenHash, s.Now().UTC())
	return t, nil
}

// resolveTarget 把路径 robotId 解析为统一管线目标键（与 gateway v1 桥接同语义：
// 已配对设备优先以配对身份入管线；否则 v1:robotId 命名空间）。
func (s *Server) resolveTarget(ctx context.Context, robotID string) (string, *apiError) {
	dev, ok, err := s.DB.GetDeviceByRobot(ctx, robotID)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, "STORE", err.Error()}
	}
	if ok {
		return dev.DeviceID, nil
	}
	return "v1:" + robotID, nil
}

// auditKindOpenAPI 兼容面审计前缀。
const auditKindOpenAPI = "openapi_v1"
