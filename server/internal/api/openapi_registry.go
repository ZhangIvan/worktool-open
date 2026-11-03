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

// openapiCommandRegistry 为兼容面指令注册表（云→端指令 201~240/244/303/304/501~509，
// 名称对齐 spec §4.2 指令表与上游指令目录）。上报/心跳类（11/101/102/301/302）与
// 上游无定义页类型（215/216/217/231/232）不注册 → 提交即 UNKNOWN_COMMAND
// （WTO-004 碰撞拒绝：裸数字不得映射为 operation）。
//
// 排除集全量登记（REV-SRV-03，防「46=除五条 N/A 外全集」误读；防漂移断言见
// TestOpenAPIRegistryExclusionSetLocked）：
//   - 11/101/102/283/301/302：上报/心跳/端侧触发类，非云→端提交面；
//   - 215/216/217/231/232：上游无定义页（双侧皆无）；
//   - 235/243：官方指令页已定案但排期 W-M6（B3 批）——235 点击聊天推荐条
//     （doc-9564531 §235）、243 二维码图片 URL 入群（doc-9564531 §243，
//     依赖 FR-56 WeChat 规则包）；实现批次前注册表不收，收时须同步
//     scheduler 分级表（docs/01 FR-53 裁决③）；
//   - 305：官方指令页已定案（api-370958736，清除指定客户端指令），但其语义由
//     REST 等价面 DELETE /openapi/v1/robots/{id}/queue/{commandId} 承接
//     （不作为可提交指令类型注册；305 入队无意义——取消对象是已入队指令）；
//   - 306/307：队列查询经 GET …/queue、配置更新为端侧事件，均非提交面；
//   - 510~515：B3/W-M6 批（docs/10 §B3），未到排期不预注册。
//
// W-M5 apifox 全量面扩容（本批）：+212/236/238（B1 批）与 +237/239/240/244
// （B3 批）。其中 212 此前经 AUD-WM3-09「刻意不注册」（端侧本地动作）；本轮按
// apifox 全量映射目标显式注册——REST 提交侧放行，风险分级仍以 scheduler
// guard.go 分级表为准（212 已在 plannedActionNames=L0；236/238/237/239/240/244
// 未登记分级表 → ClassifyRisk 按未登记 L2 fail-closed，登记后方可降级，
// docs/01 FR-53 裁决②③；scheduler 车道同步维护，本注册表不越权定级）。
var openapiCommandRegistry = []openapiIntent{
	{201, "stop_and_go_home"}, {202, "loop_receive_new_message"}, {203, "send_message"},
	{204, "reply_message"}, {205, "relay_message"}, {206, "init_group"},
	{207, "update_group"}, {208, "push_micro_disk_image"}, {209, "push_micro_disk_file"},
	{210, "push_microprogram"}, {211, "push_office"}, {212, "pass_all_friend_request"},
	{213, "add_friend_by_phone"}, {214, "show_group_info"}, {218, "push_file"},
	{219, "dismiss_group"}, {220, "add_friend_by_group"}, {221, "add_need_deal"},
	{222, "clock_in"}, {223, "switch_corp"}, {224, "push_link"},
	{225, "modify_group_member_info"}, {226, "recall_message"}, {227, "relay_multi_message"},
	{228, "relay_merge_message"}, {229, "send_multi_message"}, {230, "send_merge_message"},
	{233, "scan_qr_code"}, {234, "delete_contact"}, {236, "delete_single_directional_contact"},
	{237, "wework_clean_cache"}, {238, "send_batch_text_message"},
	{239, "create_customer_group_live_qrcode"}, {240, "add_group_board_webpage"},
	{244, "exit_group"},
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

// 上游业务错误码（ExecCallbackBean 常量全集的兼容面镜像；协议事实值，非本服务
// 自分配段——REST 信封仅在语义对应处引用，执行层码经 RAW_CONFIRM / history 面
// 透传并附 codeName 注解）。分层纪律：101018 只出现在执行回执（socketType=3
// 语义，即本面 outbox exec_result 行），501011/501012 可双栖 REST 信封与回执。
const (
	openapiUpstreamIllegalData      = 101011 // 数据格式错误（字段缺失/类型不符）
	openapiUpstreamIllegalOp        = 101012 // 非法操作（如回调地址校验未通过、清除非 queued 指令）
	openapiUpstreamRepeatInstruct   = 101014 // 指令重复（入队去重命中：同目标同 type 同载荷仍排队中）
	openapiUpstreamNetwork          = 101016 // 网络错误
	openapiUpstreamException        = 101017 // 客户端异常（不重试路径）
	openapiUpstreamInstrCancelled   = 101018 // 指令取消（清空队列/指定取消的待执行指令逐条回执）
	openapiUpstreamGroupTemplate    = 201017 // 改群模板失败
	openapiUpstreamCreateGroupLimit = 201018 // 创建群达到上限
	openapiUpstreamGroupExists      = 201019 // 创建群已存在
	openapiUpstreamAddFriendLimit   = 201020 // 加好友达到上限
	openapiUpstreamGroupExited      = 201021 // 群已退出（对照查询族同场景=0 成功，勿误修）
)

// openapiCodeNames 为上游错误码 → 中文名（RAW_CONFIRM / history 面注解用；
// 成功 0 与服务端自分配段 101001~101010 不在本表）。
var openapiCodeNames = map[int]string{
	101011: "数据格式错误", 101012: "非法操作", 101013: "非法权限", 101014: "指令重复",
	101016: "网络错误", 101017: "客户端异常", 101018: "指令取消",
	201011: "创建群失败", 201012: "群改名失败", 201013: "群拉人失败", 201014: "群踢人失败",
	201015: "改群公告失败", 201016: "改群备注失败", 201017: "改群模板失败",
	201018: "创建群达到上限", 201019: "创建群已存在", 201020: "加好友达到上限",
	201021: "群已退出", 201101: "查找聊天窗失败", 201102: "发送消息失败",
	201103: "按钮寻找失败", 201104: "目标寻找失败", 201105: "转发失败", 201106: "重复添加",
	201107: "文件下载异常", 201108: "文件存储异常", 201109: "二维码获取失败",
	501011: "机器人ID错误", 501012: "机器人Key错误", 501013: "机器人重复登录", 501000: "其他未知错误",
}

// OpenAPICodeName 返回上游业务错误码中文名（未知码返回空；供结果/历史面注解）。
func OpenAPICodeName(code int) string { return openapiCodeNames[code] }

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

// openapiLimiterKeysCap 为限流器 hits map 的 key 基数上限（REV-SRV-04：防未认证
// 随机 robotId 路径参数造成 map 无界增长——超限后对**新 key** 直接拒绝（fail-
// closed），既有 key 不受影响；配合「鉴权成功后才计数」，key 基数事实上被
// active token 数约束，此上限为纵深防御兜底）。
const openapiLimiterKeysCap = 4096

// openapiLimiter 为按 robot 的滚动窗口限流器（60QPM 默认）。计数仅在鉴权通过后
// 发生（REV-SRV-04：未认证请求不进 map、不消耗任何 robot 的配额——见
// openapiGuard / HandleOpenAPICommand / HandleOpenAPIResult 的先鉴权后限流次序）。
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
	if len(kept) == 0 {
		// 窗口内零命中：回收 entry（REV-SRV-04：不回收则每唯一 robotId 一条
		// 永不释放的空切片，构成未认证内存耗尽向量）
		if _, exists := l.hits[robotID]; exists {
			delete(l.hits, robotID)
		}
		if len(l.hits) >= openapiLimiterKeysCap {
			return false // 新 key 超基数上限：拒新保旧（fail-closed）
		}
		l.hits[robotID] = []time.Time{now}
		return true
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
	// 保留名硬拒（CR ADM-1）：robot_id='admin' 编码 admin scope——若允许为名叫
	// admin 的 v1 robot 签发兼容面令牌，将静默铸出第二个 admin-scope 令牌
	//（持有者可打全部管理面端点）。
	if robotID == sqlite.ReservedAdminScopeRobotID {
		return "", fmt.Errorf("robot_id %q 为管理面保留名（admin scope），不得签发兼容面令牌", robotID)
	}
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
	// 保留名硬拒（CR ADM-1）：对 robotId=admin 寻址恰好命中 admin 令牌的
	// t.RobotID == robotID 校验（非「寻址不匹配无意义」）——admin 令牌由此可
	// 经 resolveTarget（v1:admin 或 devices.robot_id='admin' 绑定）进入业务管线。
	// 此处先于一切校验硬拒，admin scope 只能走 /api/v1/admin/* 管理面。
	if robotID == sqlite.ReservedAdminScopeRobotID {
		return sqlite.APIToken{}, &apiError{http.StatusForbidden, "ROBOT_MISMATCH",
			"robotId=admin 为管理面保留名：admin 令牌不得用于 /openapi/v1 兼容面寻址"}
	}
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
