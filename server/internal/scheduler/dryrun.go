// 干跑模式（FR-54：指令产出"将要做什么"报告，不产生真实副作用；P1，T3.2 落地
// 服务端侧）。与真实执行同引擎同路径（docs/01 FR-54）：同一条统一命令管线、
// 同一张状态机边集（E01→E03→E05→E09→E10），仅以 dry_run 标记区分：
//   - 干跑指令绝不下发设备（store.QueuedForTarget 排除 dry_run=1，无障碍
//     副作用不可能发生）；
//   - E03/E05/E09 的证据标注 dry-run（command_events 可区分）；
//   - 报告含目标/参数/风险级（FR-54 报告要素），经 HandleResult 正常落终态、
//     入 outbox（回调可收到 dryRun 标记的结果）；
//   - 干跑结果不计入熔断统计（FR-54 干跑不污染 FR-53 风控样本）。
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// DryRunReport 为干跑报告（FR-54：目标/参数/风险级 + "将要做什么"）。
type DryRunReport struct {
	DryRun        bool           `json:"dryRun"`
	CommandID     string         `json:"commandId"`
	Target        string         `json:"target"`
	CommandType   int            `json:"commandType"`
	RiskClass     string         `json:"riskClass"`
	PlannedAction string         `json:"plannedAction"`
	Params        map[string]any `json:"params"`
	Note          string         `json:"note"`
}

// ExecuteDryRun 对一条干跑指令执行服务端干跑：沿正典边链推进到 succeeded，
// 报告经结果路径（RAW_CONFIRM 语义）落库可查。
//
// **幂等可重入（spec §5.6.1 重试安全，CR-t32-T3.2-R1 + CR-R1-1）**：按指令现态
// 续跑缺失的边——首次提交（queued）走全链 E03→E05→E09→E10；首次在链中途中断
// （进程崩溃/DB 错误，停于 dispatched/acked/running）时重调本方法从断点续跑收敛；
// 已 succeeded 时直接复建报告（零新边、零副作用）。非干跑指令报错。
// 不可收敛态（failed/cancelled/execution_unknown 等终/异常态）报错——同幂等键
// 须换新键重提。
func (s *Service) ExecuteDryRun(ctx context.Context, commandID string) (*DryRunReport, error) {
	cmd, ok, err := s.db.GetCommand(ctx, commandID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("scheduler/dryrun: 指令 %s 不存在", commandID)
	}
	if !cmd.DryRun {
		return nil, fmt.Errorf("scheduler/dryrun: 指令 %s 非干跑指令（dry_run=0，拒绝服务端代执行）", commandID)
	}

	switch cmd.State {
	case "queued":
		// E03（dispatch，证据=dry-run，无设备帧）
		if err := s.MarkDispatched(ctx, commandID, 0, 0, wire.NewUUID()); err != nil {
			return nil, fmt.Errorf("scheduler/dryrun: E03: %w", err)
		}
		fallthrough
	case "dispatched":
		if err := s.HandleTransportAck(ctx, commandID, "dry-run: 服务端代行（无设备传输）"); err != nil {
			return nil, fmt.Errorf("scheduler/dryrun: E05: %w", err)
		}
		fallthrough
	case "acked":
		if err := s.HandleExecStart(ctx, commandID, "dry-run: 服务端代行（无端侧副作用）"); err != nil {
			return nil, fmt.Errorf("scheduler/dryrun: E09: %w", err)
		}
		fallthrough
	case "running":
		report := buildDryRunReport(cmd)
		metaJSON, _ := json.Marshal(report)
		res := &wire.ExecResult{
			Type:        cmd.CommandType,
			ErrorCode:   0,
			ErrorReason: "",
			RunTime:     s.Now().UTC().Format(time.RFC3339),
			TimeCost:    0,
			SuccessList: []string{},
			FailList:    []string{},
			MetaJSON:    string(metaJSON),
		}
		state, err := s.HandleResult(ctx, commandID, res)
		if err != nil {
			return nil, fmt.Errorf("scheduler/dryrun: E10: %w", err)
		}
		if state != "succeeded" {
			return nil, fmt.Errorf("scheduler/dryrun: 干跑终态=%s（须 succeeded）", state)
		}
		return report, nil
	case "succeeded":
		// 已终态：报告要素由指令行确定性导出，复建即原响应（不重复代执行/不新增边）
		return buildDryRunReport(cmd), nil
	default:
		return nil, fmt.Errorf("scheduler/dryrun: 指令 %s 状态=%s（干跑不可从该态续跑，须换新幂等键重提）", commandID, cmd.State)
	}
}

// buildDryRunReport 由指令行构造干跑报告（要素确定性导出——同一条指令重建即原响应）。
func buildDryRunReport(cmd sqlite.Command) *DryRunReport {
	report := &DryRunReport{
		DryRun:        true,
		CommandID:     cmd.CommandID,
		Target:        cmd.Target,
		CommandType:   cmd.CommandType,
		RiskClass:     cmd.RiskClass,
		PlannedAction: plannedAction(cmd.CommandType),
		Params:        map[string]any{},
		Note:          "干跑报告：未下发设备、未产生真实副作用（FR-54）",
	}
	_ = json.Unmarshal([]byte(cmd.Payload), &report.Params)
	return report
}

// plannedAction 指令类型的"将要做什么"描述（spec §4.2 指令名）。
//
// 该名单同时是 ClassifyRisk 的「已实现指令集」（与 api 层 openapiCommandRegistry
// 同集；212 PASS_ALL_FRIEND_REQUEST 经端侧执行器实现、REST 面刻意不注册——
// docs/01 FR-53 裁决③「其余已实现指令=L0」对它成立，端侧静态表同为 L0）：
// 名单外类型 = 未登记 → L2 fail-closed（裁决②）。两处同步维护。
var plannedActionNames = map[int]string{
	201: "停止任务回首页", 202: "主循环开关", 203: "发送文本消息", 204: "引用回复消息",
	205: "转发消息", 206: "创建群（拉人/公告/备注/模板）", 207: "修改群配置（含拉人/踢人）",
	208: "推送微盘图片", 209: "推送微盘文件", 210: "推送小程序", 211: "推送腾讯文档",
	212: "通过全部待处理好友请求", 213: "按手机号添加好友", 214: "展示群信息",
	218: "推送文件", 219: "解散群",
	220: "从群添加好友", 221: "添加待办", 222: "打卡", 223: "切换企业", 224: "推送链接",
	225: "修改群成员备注/标签", 226: "撤回消息", 227: "批量转发", 228: "合并转发",
	229: "批量发送", 230: "合并发送", 233: "扫一扫", 234: "删除联系人",
	303: "接口测试", 304: "清空待执行指令队列",
	501: "查询群列表", 502: "查询好友列表", 503: "查询我的信息", 504: "获取群二维码",
	505: "查询最近会话", 506: "查询企业列表", 507: "查询全部好友", 508: "查询全部群",
	509: "上传本地文件/日志",
}

func plannedAction(commandType int) string {
	if name, ok := plannedActionNames[commandType]; ok {
		return name
	}
	return fmt.Sprintf("指令 type=%d", commandType)
}
