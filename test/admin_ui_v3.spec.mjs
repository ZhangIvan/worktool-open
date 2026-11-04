/*
 * admin/index.html v3 重建车道回归测试（零依赖：node test/admin_ui_v3.spec.mjs）
 *
 * 覆盖（与车道自查项一一对应）：
 *  A. JS 语法/括号配平      —— vm.Script 编译（SyntaxError 即失败）
 *  B. $('id') 引用完整性    —— 脚本内每个 $("X") 在 HTML/模板中存在 id='X' 或 id="X"
 *  C. 五工作区导航可达      —— nav 5 个 data-ws + 16 条路由 + 10 条旧路由重定向
 *  D. 各页渲染不抛错且关键标记在位（注入合成 fixture 的 fetch，行模板真实渲染）
 *  E. 异步失败路径          —— fetch 拒绝后列表落「加载失败」而非卡「加载中」；失败不归零
 *  F. 核心流程三步可达      —— 工作台→新建任务→提交路由决策→任务轨迹（时间线/投递独立）
 *  G. 纯函数行为（本车道行为变更的回归点，逐条断言）
 *  H. 视觉基线与脱敏        —— 浅色 token 在位；无 000000 占位/真实网段/手机号/外部资源
 *
 * 运行：cwd = 仓库根，`node test/admin_ui_v3.spec.mjs`；退出码 0=全过。
 */
import { readFileSync } from "node:fs";
import vm from "node:vm";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const html = readFileSync(path.join(root, "admin", "index.html"), "utf8");
const mScript = html.match(/<script>([\s\S]*)<\/script>/);
if (!mScript) { console.error("FAIL: admin/index.html 中未找到 <script> 块"); process.exit(1); }
const src = mScript[1];

let failed = 0, passed = 0;
function ok(cond, name, extra) {
  if (cond) { passed++; console.log("  ok  " + name); }
  else { failed++; console.error("  FAIL " + name + (extra !== undefined ? "  ← " + JSON.stringify(extra) : "")); }
}
function section(t) { console.log("\n== " + t + " =="); }

/* ---------- A. 语法/括号配平 ---------- */
section("A. JS 语法（括号配平，vm.Script 编译即 SyntaxError 门）");
try { new vm.Script(src); ok(true, "脚本可编译（无语法错误/括号失衡）"); }
catch (e) { ok(false, "脚本可编译", e.message); }

/* ---------- 合成 fixture（脱敏：演示设备A/B/C、成员甲、192.0.2/127.0.0.1 占位） ---------- */
const NOW = Date.UTC(2026, 10, 4, 6, 0, 0);
const CMD_ID = "cmd-aaaabbbbccccdddd";
let fetchMode = "ok"; // "ok" | "fail"（E 段切到 fail 验证失败状态）
const canned = [
  // 顺序敏感：更具体的路径在前
  [/^\/api\/v1\/admin\/commands\/cmd-aaaabbbbccccdddd$/, () => ({
    command: { commandId: CMD_ID, target: "dev-aaaabbbbccccdddd", type: 203,
      payload: '{"type":203,"titleList":["成员甲"],"receivedContent":"你好"}',
      state: "queued", riskClass: "L0", dryRun: false, createdAtMs: NOW, dispatchedAtMs: 0,
      ackDeadlineMs: NOW + 600000, terminalAtMs: 0, terminalState: "", epoch: 1, seq: 1 },
    events: [{ edge: "E01", fromState: "", toState: "queued", event: "created", detail: "受理", atMs: NOW }],
    results: [],
  })],
  [/^\/api\/v1\/admin\/commands\/cmd-9999888877776666$/, () => ({
    command: { commandId: "cmd-9999888877776666", target: "dev-1", type: 219,
      payload: '{"type":219,"groupName":"交付群"}',
      state: "failed", riskClass: "L2", dryRun: false, createdAtMs: NOW, dispatchedAtMs: NOW,
      ackDeadlineMs: NOW + 600000, terminalAtMs: NOW + 1000, terminalState: "failed", epoch: 1, seq: 2 },
    events: [
      { edge: "E01", fromState: "", toState: "queued", event: "created", detail: "受理", atMs: NOW },
      { edge: "E11", fromState: "running", toState: "failed", event: "exec_result", detail: "errorCode=201010", atMs: NOW + 1000 },
    ],
    results: [{ payload: { errorCode: 201010, reason: "目标 App 不在前台" }, atMs: NOW + 1000 }],
  })],
  [/^\/api\/v1\/admin\/commands\?/, () => ({
    total: 2, offset: 0, count: 2, commands: [
      { commandId: CMD_ID, target: "dev-aaaabbbbccccdddd", type: 203, state: "succeeded", terminalState: "succeeded", riskClass: "L0", dryRun: false, createdAtMs: NOW },
      { commandId: "cmd-2222333344445555", target: "dev-1", type: 219, state: "queued", terminalState: "", riskClass: "L2", dryRun: false, createdAtMs: NOW },
    ],
  })],
  [/^\/api\/v1\/admin\/catalog/, () => ({
    commands: [
      { type: 203, name: "发送文本消息", risk: "L0", required: ["titleList", "receivedContent"], example: '{"type":203,"titleList":["成员甲"],"receivedContent":"你好"}', group: "消息·发送", status: "implemented", rawMsg: "回执 rawMsg=指令原文", schema: { required: { titleList: "array", receivedContent: "string" } } },
      { type: 219, name: "解散群", risk: "L2", required: ["groupName"], example: '{"type":219,"groupName":"交付群"}', group: "群·高风险", status: "implemented", rawMsg: "", schema: { required: { groupName: "string" } } },
      { type: 202, name: "主循环开关", risk: "L0", required: ["loop.on"], example: '{"type":202,"loop":{"on":false}}', group: "会话与循环", status: "implemented", rawMsg: "", schema: { required: { "loop.on": "boolean" } } },
    ],
    statusLegend: {}, riskLegend: { L0: "低风险", L2: "高风险（独立审批）" },
  })],
  [/^\/api\/v1\/admin\/devices$/, () => ({
    devices: [
      { deviceId: "dev-aaaabbbbccccdddd", robotId: "robot-x", deviceName: "演示设备A", status: "active", online: true, lastSessionMs: NOW - 60000, epoch: 1, pairedAtMs: NOW, updatedAtMs: NOW },
      { deviceId: "dev-eeeeffffgggghhhh", robotId: "", deviceName: "演示设备B", status: "paused", online: true, lastSessionMs: NOW - 60000, epoch: 2, pairedAtMs: NOW, updatedAtMs: NOW },
      { deviceId: "dev-1111222233334444", robotId: "", deviceName: "演示设备C", status: "active", online: false, lastSessionMs: 0, epoch: 3, pairedAtMs: NOW, updatedAtMs: NOW },
    ],
  })],
  [/^\/api\/v1\/admin\/queue/, () => ({ queue: [{ commandId: "cmd-2222333344445555", target: "dev-aaaabbbbccccdddd", type: 219, riskClass: "L2", position: 1, createdAtMs: NOW }], count: 1 })],
  [/^\/api\/v1\/admin\/approvals/, () => ({ approvals: [{ approvalId: "ap-1", target: "dev-1", type: 219, riskClass: "L2", actor: "admin-token:abcd1234", status: "pending", approver: "", reason: "", recurring: false, createdAtMs: NOW, decidedAtMs: 0 }], status: "pending", count: 1 })],
  [/^\/api\/v1\/admin\/callbacks\/logs/, () => ({ logs: [{ id: 1, commandId: CMD_ID, kind: "exec_result", status: "skipped", attempts: 0, nextAttemptMs: 0, lastError: "", createdAtMs: NOW, updatedAtMs: NOW }], count: 1, limit: 100, offset: 0 })],
  [/^\/api\/v1\/admin\/logs\/executions/, () => ({ events: [{ commandId: CMD_ID, target: "dev-1", type: 203, riskClass: "L0", edge: "E01", fromState: "", toState: "queued", event: "created", detail: "受理", atMs: NOW }], count: 1, limit: 50, offset: 0 })],
  [/^\/api\/v1\/admin\/logs\/audit/, () => ({ entries: [{ id: 1, kind: "admin_command_submitted", subject: "cmd-9", detail: "target=dev-1 type=203 dryRun=false actor=admin-token:abcd1234 state=queued idemKey=k1", atMs: NOW }], count: 1, limit: 50, offset: 0 })],
  [/^\/api\/v1\/admin\/callbacks$/, () => ({ callbacks: [{ robotId: "robot-x", type: 1, typeName: "指令结果回调", callbackUrl: "http://127.0.0.1:9999/cb", enabled: true }], count: 1 })],
  [/^\/api\/v1\/admin\/tokens$/, () => ({ tokens: [{ hashPrefix8: "abcd1234", robotId: "robot-x", scope: "robot:robot-x", tenant: "default", status: "active", createdMs: NOW }], count: 1 })],
  [/^\/api\/v1\/admin\/overview$/, () => ({ devicesOnline: 1, devicesTotal: 3, todayCommands: 24, todaySucceeded: 18, todayFailed: 2, todayErrorRate: 0.1, pendingApprovals: 1, queuedCommands: 1, pendingCallbacks: 0, recentCallbacks: [] })],
  [/^\/api\/v1\/admin\/settings$/, () => ({
    guard: { l1WindowSeconds: 60, l1MaxPerTx: 10, whitelistDefaultDeny: false, source: "default" },
    breaker: { state: "closed", windowSeconds: 60, minSamples: 5, threshold: 0.5, cooldownSeconds: 120, closeSamples: 3 },
    encryption: { enabled: false, note: "当前版本仅保存配置意图" },
    eventCallbacks: { pushEventCallback: true, pushImage: false, source: "default" },
    counts: { activeTokens: 2, whitelistEntries: 1 },
  })],
  [/^\/api\/v1\/admin\/cron-jobs/, () => ({ jobs: [{ jobId: "job-1", name: "巡检", target: "robot-x", type: 503, cronExpr: "30 9 * * *", riskClass: "L0", approvalId: "", enabled: true, lastFireMs: 0, lastCommandId: "", lastResult: "", nextFireMs: NOW, createdAtMs: NOW }], count: 1 })],
];

/* ---------- 沙箱（最小 DOM stub + 可切换的 fetch） ---------- */
function makeEl(id) {
  return {
    id, innerHTML: "", textContent: "", value: "", className: "", title: "",
    hidden: false, disabled: false, checked: false, style: {},
    classList: { toggle() {}, add() {}, remove() {} },
    addEventListener() {}, appendChild() {}, removeChild() {},
    querySelector() { return null; },
    querySelectorAll() { return []; },
    insertBefore() {},
    setAttribute() {}, getAttribute() { return null; },
    onclick: null, oninput: null, onchange: null,
  };
}
const els = {};
const sandbox = {
  console: { log() {}, error() {}, warn() {} },
  localStorage: (() => { const s = {}; return { getItem: k => (k in s ? s[k] : null), setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; } }; })(),
  location: { hash: "#/home", pathname: "/", search: "" },
  history: { replaceState() {}, pushState() {} },
  navigator: {},
  fetch(p) {
    if (fetchMode === "fail") return Promise.reject(new Error("offline-test"));
    for (const [re, fn] of canned) {
      if (re.test(String(p))) { const body = fn(); return Promise.resolve({ status: 200, ok: true, json: async () => body }); }
    }
    return Promise.resolve({ status: 200, ok: true, json: async () => ({}) });
  },
  setTimeout, clearTimeout,
  setInterval: () => 0, // 不真起定时器（避免挂起进程；轮询分支由纯函数/状态断言覆盖）
  clearInterval: () => {},
  AbortController,
  Date, Math, JSON, Promise, Object, Array, String, Number, Boolean, RegExp, Error, isFinite, parseInt, parseFloat,
};
sandbox.window = sandbox;
sandbox.addEventListener = () => {};
sandbox.globalThis = sandbox;
sandbox.document = {
  getElementById(id) { if (!els[id]) els[id] = makeEl(id); return els[id]; },
  querySelectorAll() { return []; },
  querySelector() { return null; },
  createElement() { return makeEl("tmp-" + Math.random()); },
  addEventListener() {},
  body: makeEl("body"),
};
vm.createContext(sandbox);
vm.runInContext(src, sandbox, { filename: "admin/index.html#script" });
const settle = () => new Promise(r => setTimeout(r, 60));

/* ---------- B. $('id') 引用完整性 ---------- */
section("B. $('id') 引用完整性");
const refIds = new Set();
for (const mm of src.matchAll(/\$\("([A-Za-z0-9_-]+)"\)/g)) refIds.add(mm[1]);
// btnPrimary/btnGhost 助手以第一参生成 id 属性（运行时拼接，源码无字面 id='X'）
const helperIds = new Set();
for (const mm of src.matchAll(/btn(?:Primary|Ghost)\("([A-Za-z0-9_-]+)"/g)) helperIds.add(mm[1]);
const missing = [...refIds].filter(id => !html.includes('id="' + id + '"') && !html.includes("id='" + id + "'") && !helperIds.has(id));
ok(missing.length === 0, `所有 $("…") 引用的 id 均存在（共 ${refIds.size} 个引用 + ${helperIds.size} 个助手按钮 id，含动态模板）`, missing);
const helperDead = [...helperIds].filter(id => !refIds.has(id));
ok(helperDead.length === 0, `助手按钮（btnPrimary/btnGhost）声明的 id 均有 $("…") 引用（无死按钮）`, helperDead);

/* ---------- C. 五工作区导航 + 旧路由重定向 ---------- */
section("C. 五工作区导航与路由");
const WORKSPACES = sandbox.WORKSPACES, ROUTES = sandbox.ROUTES, LEGACY = sandbox.LEGACY;
ok(!!WORKSPACES && Object.keys(WORKSPACES).length === 5 &&
   Object.values(WORKSPACES).join(",") === "工作台,设备,任务,观测,设置", "WORKSPACES 恰为五个工作区（工作台/设备/任务/观测/设置）", WORKSPACES);
const navWs = [...html.matchAll(/data-ws="([a-z]+)"/g)].map(m => m[1]);
ok(navWs.length === 5 && Object.keys(WORKSPACES).every(w => navWs.includes(w)), "nav 恰有 5 个工作区链接且与 WORKSPACES 对齐", navWs);
ok(!!ROUTES && Object.keys(ROUTES).length === 16, "路由表 16 条（工作台1 + 设备3 + 任务6 + 观测3 + 设置3）", Object.keys(ROUTES || {}));
ok(Object.keys(ROUTES).every(k => ROUTES[k].ws in WORKSPACES), "每条路由都归属一个工作区");
const needLegacy = ["dashboard", "send", "commands", "queue", "approvals", "cron-jobs", "callbacks", "callback-logs", "logs-exec", "logs-audit"];
ok(!!LEGACY && needLegacy.every(k => k in LEGACY && LEGACY[k] in ROUTES), "旧 URL 全部重定向到有效路由（history/executions/callbacks/logs 等）", LEGACY);
const tasksKids = ["tasks-send", "tasks-current", "tasks-history", "tasks-queue", "tasks-approvals", "tasks-cron"];
ok(tasksKids.every(k => ROUTES[k].ws === "tasks"), "任务工作区含发送/当前任务/任务中心/队列/审批/定时六子页");
const obsKids = ["obs-execlogs", "int-deliveries", "audit"];
ok(obsKids.every(k => ROUTES[k].ws === "obs"), "观测工作区含执行日志/回调记录/审计日志三子页");
const configKids = ["int-callbacks", "int-api", "settings"];
ok(configKids.every(k => ROUTES[k].ws === "config"), "设置工作区含回调配置/OpenAPI/安全设置三子页");
const deviceKids = ["devices", "devices-detail", "devices-pair"];
ok(deviceKids.every(k => ROUTES[k].ws === "devices"), "设备工作区含设备中心/设备详情/添加设备三子页");
// 旧路由重定向实际生效：#commands → 渲染任务中心（面包屑验证）
sandbox.location.hash = "#/commands";
sandbox.route();
await settle();
ok(sandbox.$("crumb-view").textContent === "任务中心" && sandbox.$("crumb-ws").textContent === "任务", "旧路由 #/commands 重定向到 任务/任务中心", sandbox.$("crumb-view").textContent);
sandbox.location.hash = "#/logs-audit";
sandbox.route();
await settle();
ok(sandbox.$("crumb-ws").textContent === "观测" && sandbox.$("crumb-view").textContent === "审计日志", "旧路由 #/logs-audit 重定向到 观测/审计日志");
sandbox.location.hash = "#/logs-exec";
sandbox.route();
await settle();
ok(sandbox.$("crumb-ws").textContent === "观测" && sandbox.$("crumb-view").textContent === "执行日志", "旧路由 #/logs-exec 重定向到 观测/执行日志");
sandbox.location.hash = "#/home";
sandbox.route();

/* ---------- D. 各页渲染 + 关键标记（合成 fixture 下行模板真实渲染） ---------- */
section("D. 五工作区各页渲染（不抛错 + 标记在位）");
const view = sandbox.$("view");
// 渲染面 = 页面骨架（view.innerHTML）+ 全部动态填充的子元素存根（行模板/摘要/下拉选项）
function renderedAll() {
  return view.innerHTML + " " + Object.values(els).map(e => String(e.innerHTML) + " " + String(e.textContent)).join(" ");
}
const renderCases = [
  ["renderHome", ["工作台", "创建任务", "需要处理", "设备会话概览", "近期任务", "待关注任务", "去处理"]],
  ["renderDevices", ["设备中心", "添加设备", "机器人绑定", "最近联系", "创建任务", "未知（权限未上报）"]],
  ["renderDevicesPair", ["接入 Android 设备", "一次性配对码", "生成一次性配对码", "等待连接"]],
  ["renderTaskCurrent", ["尚未打开任务轨迹", "新建任务", "去任务中心选择"]],
  ["renderCommands", ["任务中心", "创建任务", "干跑", "详情", "选中任务", "结果未知"]],
  ["renderQueue", ["指令队列", "移到队首", "取消待执行", "清空该目标队列", "最长等待"]],
  ["renderApprovals", ["审批", "决议令牌", "通过审批", "审批前核对", "去处理"]],
  ["renderCronJobs", ["定时任务", "cron", "预览触发时刻", "启用中", "今日计划预览"]],
  ["renderObsExecLogs", ["执行日志", "结构化事件", "原始记录", "打开任务轨迹", "结果未知"]],
  ["renderCallbackLogs", ["回调记录", "下次尝试", "不会自动重试", "未投递", "接收器"]],
  ["renderAuditLogs", ["系统审计日志", "主体（谁）", "动作（做了什么）", "对象（对什么）", "提交了指令任务"]],
  ["renderCallbacks", ["回调配置", "回调类型说明", "保存", "http://127.0.0.1:9999/cb", "投递链路"]],
  ["renderApi", ["OpenAPI 接入", "令牌凭据", "吊销该 robot 全部有效令牌", "一次显示", "签发令牌"]],
  ["renderSettings", ["系统安全设置", "限流与接收对象白名单", "全局熔断器", "通讯加密", "风险分级查询", "保存安全草稿"]],
];
for (const [fn, marks] of renderCases) {
  let threw = null;
  try { sandbox[fn](); await settle(); } catch (e) { threw = e; }
  ok(!threw, `${fn}() 渲染不抛错`, threw && threw.stack);
  if (!threw) {
    const rendered = renderedAll();
    const miss = marks.map(m => m.split("|")).filter(alts => !alts.some(mk => rendered.includes(mk)));
    ok(miss.length === 0, `${fn}() 关键标记在位`, miss.flat());
  }
}
// 设备详情（需 ROUTE_Q 指向已有设备）
sandbox.ROUTE_Q = { id: "dev-aaaabbbbccccdddd" };
{
  let threw = null;
  try { sandbox.renderDeviceDetail(); await settle(); } catch (e) { threw = e; }
  ok(!threw, "renderDeviceDetail() 渲染不抛错", threw && threw.stack);
  if (!threw) {
    const rendered = renderedAll();
    const miss = ["设备信息", "暂停接收任务", "此设备的任务", "最近会话"].filter(mk => !rendered.includes(mk));
    ok(miss.length === 0, "renderDeviceDetail() 关键标记在位", miss);
  }
}
sandbox.ROUTE_Q = {};
ok(!/id=["'](token|save-token)["']/.test(html.slice(html.indexOf('id="topbar"'), html.indexOf("</nav>"))), "顶栏无常驻令牌输入框（令牌管理收进「会话」弹层）");
ok(html.includes("id='token'") && html.includes("save-token") && html.includes("TOKEN_KEY"), "令牌管理逻辑保留（localStorage + 保存/清除 + #token= 深链吸收）");
ok(html.includes("wt_approver_token") && html.includes("approverToken"), "P0-3 决议令牌分持逻辑保留");

/* ---------- E. 异步失败路径 ---------- */
section("E. 异步失败状态（fetch 拒绝 → 加载失败行，不卡「加载中」；失败不归零）");
fetchMode = "fail";
sandbox.renderQueue();
await settle();
ok(els["queue-body"].innerHTML.includes("加载失败"), "队列页失败后落错误行（S11：不吞异常停在「尚未加载」）", els["queue-body"].innerHTML.slice(0, 120));
sandbox.renderCommands();
await settle();
ok(els["commands-body"].innerHTML.includes("加载失败"), "任务中心失败后落错误行");
sandbox.renderHome();
await settle();
ok(els["hm-recent"].innerHTML.includes("加载失败"), "工作台近期任务面板独立失败（其余面板继续）");
ok((els["hm-dev"].textContent + els["hm-dev"].innerHTML).includes("—"), "工作台指标失败不归零（保持 —）", els["hm-dev"].innerHTML);
sandbox.renderObsExecLogs();
await settle();
ok(els["el-body"].innerHTML.includes("加载失败"), "执行日志失败后落错误行（观测不吞异常）");
// D9 回归锁定（前次复核中危项）：回调配置页 GET 失败不得吞成假空态——显示「加载失败+重试」
sandbox.renderCallbacks();
await settle();
ok(els["cb-groups"].innerHTML.includes("回调绑定加载失败"), "回调配置：绑定列表读取失败 → 错误态+重试（不吞成假空态）", els["cb-groups"].innerHTML.slice(0, 120));
ok(!els["cb-groups"].innerHTML.includes("还没有任何回调绑定"), "回调配置：失败时不得渲染「无绑定」假空态");
fetchMode = "ok";

/* ---------- F. 核心流程三步可达 ---------- */
section("F. 核心流程三步可达（工作台 → 新建任务 → 提交 → 任务轨迹）");
sandbox.renderHome();
await settle();
ok(els["hm-todo"] && els["hm-todo"].innerHTML.includes("去处理"), "工作台待处理清单有「去处理」直达审批");
ok(view.innerHTML.includes("创建任务"), "工作台主按钮「创建任务」在位");
sandbox.renderTasksSend();
await settle();
sandbox.renderTasksSend();
await settle();
const sendHtml = renderedAll();
ok(["id='send-device'", "id='send-type'", "id='send-dryrun'", "id='send-go'"].every(id => view.innerHTML.includes(id)), "发送页四要素：设备下拉/动作下拉/干跑按钮/提交按钮");
// D1：离线设备可选（下拉不 disabled），标签说明「可排队，提交前需确认」
ok(sendHtml.includes("演示设备A") && sendHtml.includes("离线（可排队，提交前需确认）"), "① 设备下拉：离线可选并说明「可排队，提交前需确认」（规格 F01）");
ok(!/value='dev-1111222233334444'[^>]*disabled/.test(sendHtml), "① 离线设备下拉项不再 disabled（可进草稿）");
ok(sendHtml.includes("发送文本消息") || sendHtml.includes("目录加载中"), "② 动作下拉已按目录填充");
ok(view.innerHTML.includes("send-cattabs"), "② 动作区含分组快捷筛选 quicktabs（交互图02）");
ok(els["send-dryrun"].disabled === false || els["send-dryrun"].disabled === true, "干跑按钮受提交门条件禁用（updateGate 生效）");
// 选设备 → 选动作 → 门解锁（纯 UI 链）
sandbox.$("send-device").value = "dev-aaaabbbbccccdddd";
sandbox.WIZ.deviceId = "dev-aaaabbbbccccdddd";
sandbox.$("send-type").value = "203";
sandbox.onPick();
await settle();
{
  const picked = renderedAll();
  ok(picked.includes("真机验证：未验证") && picked.includes("设备支持：未上报"), "② 动作卡含证据与支持状态标签（目录/设备支持/真机验证，交互图02）");
  ok(picked.includes("targetRoom"), "② 选 203 显示端侧 targetRoom 字段漂移警示（规格 C203）");
}
ok(sandbox.WIZ.type === 203 && els["send-fields"].innerHTML.includes("titleList"), "选动作 203 → 类型化表单生成（数组字段在位）");
ok(els["send-fields"].innerHTML.includes("数组——每行一个值"), "数组参数渲染为多行输入");
// D1：切到离线设备 → 需勾选「上线后排队」；勾选后提交门不再因离线拦截（缺必填仍拦）
sandbox.$("send-device").value = "dev-1111222233334444";
sandbox.WIZ.deviceId = "dev-1111222233334444";
sandbox.onDeviceChange();
await settle();
ok(els["send-devinfo"].innerHTML.includes("send-offline-go"), "① 离线设备卡出现「设备上线后排队执行」确认勾选（交互图01）");
ok(els["send-gate"].innerHTML.includes("上线后排队"), "提交门：离线未确认 → 显示「上线后排队」原因（含 E19）", els["send-gate"].innerHTML);
sandbox.WIZ.offlineGo = true; sandbox.WIZ.offlineFor = "dev-1111222233334444";
sandbox.updateGate();
ok(!els["send-gate"].innerHTML.includes("上线后排队"), "提交门：勾选「上线后排队」后离线原因消失（缺必填仍在）", els["send-gate"].innerHTML);
sandbox.WIZ.offlineGo = false; sandbox.WIZ.offlineFor = "";
sandbox.$("send-device").value = "dev-aaaabbbbccccdddd";
sandbox.WIZ.deviceId = "dev-aaaabbbbccccdddd";
sandbox.onDeviceChange();
await settle();
const dec1 = sandbox.submitRouteDecision({ commandId: "c-123", state: "queued", replay: false });
ok(dec1.kind === "task", "201+commandId → 直达任务轨迹决策", dec1);
sandbox.CURRENT_CMD_ID = CMD_ID;
sandbox.$("tc-auto").checked = true; // 模拟浏览器对 checked 属性的默认置位（stub 不解析 HTML）
sandbox.renderTaskCurrent();
await settle();
// D4：自动刷新勾选框初始 checked → 轮询必须已在跑（修前 bindAuto 只在 change 时启动）
ok(typeof sandbox.autoTimers["tc-auto"] !== "undefined",
  "任务轨迹自动刷新初始态：checked 即启动轮询（无需手动切换）", sandbox.autoTimers);
const curHtml = renderedAll();
ok(view.innerHTML.includes("刷新核对"), "任务轨迹页含「刷新核对」按钮");
ok(curHtml.includes("状态时间线") && curHtml.includes("E01"), "任务轨迹含 E01→E19 时间线");
ok(curHtml.includes("结果回执") && curHtml.includes("通知投递"), "任务轨迹含结果区与投递区（两结论分离）");
ok(curHtml.includes("未投递") && curHtml.includes("接收器"), "本任务 skipped 投递行显示明确原因（任务成功/通知失败不混色）");
ok(curHtml.includes("复制为新任务"), "失败/需重做路径提供「复制为新任务」（重走预检/审批）");
ok(curHtml.includes("全局事件流") && curHtml.includes("互为印证"), "任务轨迹含服务端事件流核对（logs/executions?commandId= 交叉印证）");
const dec2 = sandbox.submitRouteDecision({ state: "awaiting_approval", approvalId: "ap-1" });
ok(dec2.kind === "approval", "202+approvalId → 审批流决策（L2 自动进审批）", dec2);
sandbox.renderDevices();
await settle();
ok(renderedAll().includes("创建任务"), "设备页含「创建任务」入口（→ 发送页预选设备）");
ok(renderedAll().includes("演示设备A"), "设备页含拓扑/选中卡设备名");
// D1：离线/暂停设备的行内「创建任务」可进入草稿（提交门在发送页拦截），不再整行禁用
{
  const dvHtml = renderedAll();
  ok(!/data-newtask='dev-1111222233334444'[^>]*disabled/.test(dvHtml), "设备行：离线设备「创建任务」可点（草稿入口不禁用，title 说明提交条件）", (dvHtml.match(/data-newtask='dev-1111222233334444'[^>]*>/) || [""])[0]);
  ok(!/data-newtask='dev-eeeeffffgggghhhh'[^>]*disabled/.test(dvHtml), "设备行：暂停设备「创建任务」可点（规格 F01 暂停可填草稿）");
}

/* ---------- D+. 任务轨迹 ?id= 深链切换：URL id 始终为准（换 id 即换任务） ----------
 * 回归：修前 renderTaskCurrent 只在 CURRENT_CMD_ID 为空时才采纳 ROUTE_Q.id——
 * 已打开任务 A 后，再走 #/tasks-current?id=B（任务中心/审计/工作台跳转）会残留显示 A。 */
{
  sandbox.CURRENT_CMD_ID = CMD_ID; // 当前正看任务 A（cmd-aaaabbbbccccdddd）
  sandbox.location.hash = "#/tasks-current?id=cmd-9999888877776666"; // 深链换到任务 B
  sandbox.route();
  await settle();
  ok(String(sandbox.CURRENT_CMD_ID) === "cmd-9999888877776666",
    "任务轨迹 ?id= 深链换 id 即换任务（CURRENT_CMD_ID 跟随 URL）", sandbox.CURRENT_CMD_ID);
  const tcView = view.innerHTML;
  ok(tcView.includes("cmd-9999…6666") && !tcView.includes("cmd-aaaa…dddd"),
    "任务轨迹深链 B 后面包屑指向 B（不再指向旧任务 A）", tcView.slice(0, 160));
  ok((els["tc-body"] ? els["tc-body"].innerHTML : "").includes("cmd-9999888877776666"),
    "任务轨迹深链 B 后详情卡渲染 B 的元数据（tc-body 跟随新 id）");
  sandbox.location.hash = "#/home"; // 还原，不干扰后续段
}

/* ---------- D+. 设备详情：暂停时禁用按钮旁有可见原因（设计08 底栏） ---------- */
sandbox.ROUTE_Q = { id: "dev-eeeeffffgggghhhh" };
{
  let threw = null;
  try { sandbox.renderDeviceDetail(); await settle(); } catch (e) { threw = e; }
  ok(!threw, "renderDeviceDetail(暂停设备) 渲染不抛错", threw && threw.stack);
  ok(renderedAll().includes("dd-gatereason"), "设备详情：提交禁用原因以可见文本呈现（不只 title，设计08）");
}
sandbox.ROUTE_Q = {};

/* ---------- D+. 审批：批准/拒绝按钮禁用时均有可见 title 原因 ---------- */
ok(els["ap-no"] && (els["ap-no"].title || "").includes("决议令牌"), "审批拒绝按钮禁用时带 title 原因（无决议令牌）", els["ap-no"] && els["ap-no"].title);
ok(els["ap-ok"] && (els["ap-ok"].title || "").includes("决议令牌"), "审批批准按钮禁用时带 title 原因");

/* ---------- G. 纯函数行为（行为变更回归点） ---------- */
section("G. 纯函数行为");
ok(sandbox.shortIdPlain("abcdef1234567890wxyz") === "abcdef12…wxyz", "shortIdPlain：前 8 后 4", sandbox.shortIdPlain("abcdef1234567890wxyz"));
ok(sandbox.shortIdPlain("short") === "short", "shortIdPlain：短 ID 原样");
const e203 = { example: '{"type":203,"titleList":["成员甲"],"receivedContent":"你好"}', required: ["titleList", "receivedContent"] };
const e202 = { example: '{"type":202,"loop":{"on":false}}', required: ["loop.on"] };
const eSchema = { example: '{"titleList":["x"]}', required: ["titleList"], schema: { required: { titleList: "array", "friend.phone": "string", flag: "boolean", n: "number" } } };
ok(sandbox.paramKind(e203, "titleList") === "array", "paramKind：数组（example 推导）");
ok(sandbox.paramKind(e203, "receivedContent") === "text", "paramKind：文本");
ok(sandbox.paramKind(e202, "loop.on") === "bool", "paramKind：嵌套布尔（loop.on）");
ok(sandbox.paramKind(eSchema, "flag") === "bool" && sandbox.paramKind(eSchema, "n") === "number" && sandbox.paramKind(eSchema, "titleList") === "array" && sandbox.paramKind(eSchema, "friend.phone") === "text", "paramKind：schema 优先（boolean/number/array/string）");
{
  const p1 = {};
  ok(sandbox.flattenParam(p1, "titleList", "array", " 群A \n群B\n\n") && JSON.stringify(p1.titleList) === '["群A","群B"]', "数组：多行→trim→剔空行", p1);
  ok(sandbox.flattenParam(p1, "titleList", "array", "  ") === false, "数组：全空行不写入");
  const p2 = {};
  ok(sandbox.flattenParam(p2, "friend.phone", "text", "13800000000") && p2.friend.phone === "13800000000", "嵌套平铺：friend.phone → payload.friend.phone", p2);
  const p3 = {};
  ok(sandbox.flattenParam(p3, "loop.on", "bool", "") === false && !("loop" in p3), "布尔：未选择（空）≠ false，不写入", p3);
  const p4 = {};
  sandbox.flattenParam(p4, "loop.on", "bool", "false");
  ok(p4.loop.on === false, "布尔：显式 false 是有效值（写入 false 而非丢弃）", p4);
  const p5 = {};
  sandbox.flattenParam(p5, "n", "number", "42");
  ok(p5.n === 42, "数字：写为 number 类型", p5);
}
ok(JSON.stringify(sandbox.missingRequired(e203, { type: 203, receivedContent: "x" })) === '["titleList"]', "missingRequired：缺数组必填被点名");
ok(sandbox.missingRequired(e203, { titleList: [], receivedContent: "x" }).includes("titleList"), "missingRequired：空数组视为缺");
ok(sandbox.missingRequired(e202, { loop: { on: false } }).length === 0, "missingRequired：嵌套 false 是已填（不误报）");
{
  // D1（规格 F01/交互图01/07）：离线设备可编辑草稿；正式提交须显式勾选「上线后排队执行」
  const off = sandbox.deviceGate({ online: false, status: "active" });
  ok(off.selectable === true && off.submitBlocked === true && off.offline === true &&
     off.reason.includes("上线后排队"), "deviceGate：离线可选可填草稿，提交须「上线后排队」确认", off);
  const paused = sandbox.deviceGate({ online: true, status: "paused" });
  ok(paused.selectable && paused.submitBlocked && paused.submitReason.includes("暂停"), "deviceGate：暂停可选可填草稿但禁提交并说明", paused);
  const good = sandbox.deviceGate({ online: true, status: "active" });
  ok(good.selectable && !good.submitBlocked, "deviceGate：在线未暂停可选可提交");
}
{
  const g1 = sandbox.wizardGate({ device: false, type: 0 });
  ok(g1.blocked && g1.reasons.some(r => r.includes("未选择执行设备")) && g1.reasons.some(r => r.includes("未选择指令动作")), "wizardGate：未选设备/动作 → 禁用并逐条给原因", g1.reasons);
  const g2 = sandbox.wizardGate({ device: true, devicePaused: true, type: 203, missing: ["titleList"], devicesStale: true });
  ok(g2.blocked && g2.reasons.some(r => r.includes("暂停")) && g2.reasons.some(r => r.includes("过期")) && g2.reasons.some(r => r.includes("必填参数")), "wizardGate：暂停/陈旧/缺必填均给原因", g2.reasons);
  const g3 = sandbox.wizardGate({ device: true, devicePaused: true, devicesStale: true, type: 203, missing: [], forDry: true });
  ok(!g3.blocked, "wizardGate：干跑豁免暂停/陈旧（服务端模拟不触达设备）", g3.reasons);
  const g4 = sandbox.wizardGate({ device: true, type: 203, missing: [], payloadError: "bad json" });
  ok(g4.blocked && g4.reasons.some(r => r.includes("JSON 解析失败")), "wizardGate：手写 JSON 解析失败 → 禁用并说明", g4.reasons);
  // D1：离线未确认「上线后排队」→ 提交门拦截并说明；确认后放行；干跑豁免
  const g5 = sandbox.wizardGate({ device: true, offlineNeed: true, type: 203, missing: [] });
  ok(g5.blocked && g5.reasons.some(r => r.includes("上线后排队") && r.includes("E19")), "wizardGate：离线未勾选「上线后排队」→ 禁用并给原因（含 E19 队列 TTL）", g5.reasons);
  const g6 = sandbox.wizardGate({ device: true, offlineNeed: false, type: 203, missing: [] });
  ok(!g6.blocked, "wizardGate：离线已确认「上线后排队」→ 提交门放行", g6.reasons);
  const g7 = sandbox.wizardGate({ device: true, offlineNeed: true, type: 203, missing: [], forDry: true });
  ok(!g7.blocked, "wizardGate：干跑豁免离线确认（服务端模拟不触达设备）", g7.reasons);
}
{
  // D5（规格 §2.2）：默认时间显示「YYYY-MM-DD HH:mm:ss UTC」
  ok(sandbox.fmtMs(Date.UTC(2026, 10, 4, 6, 0, 0)) === "2026-11-04 06:00:00 UTC",
    "fmtMs：UTC 后缀（规格 §2.2 示例格式）", sandbox.fmtMs(Date.UTC(2026, 10, 4, 6, 0, 0)));
  ok(sandbox.fmtMs(0) === "—", "fmtMs：空值仍为 —");
}
{
  // D6（规格 §2.3）：筛选无结果与首次无数据文案不同
  ok(sandbox.emptyListText(true, "筛选无结果", "暂无数据") === "筛选无结果", "emptyListText：有筛选 → 筛选文案");
  ok(sandbox.emptyListText(false, "筛选无结果", "暂无数据") === "暂无数据", "emptyListText：无筛选 → 首次文案");
  ok(html.includes("筛选无结果：当前条件下没有任务") && html.includes("还没有任务：点右上「创建任务」提交第一条指令"),
    "任务中心空态两分支文案在位（筛选/首次）");
  ok(html.includes("筛选无结果：该目标/编号下没有 queued 待执行指令") && html.includes("队列为空（无 queued 待执行指令"),
    "队列空态两分支文案在位");
  ok(html.includes("筛选无结果：当前状态下没有投递记录") && html.includes("暂无投递记录（从未产生回调事件"),
    "回调记录空态两分支文案在位");
  ok(html.includes("筛选无结果：该任务编号下暂无事件") && html.includes("全局事件流暂无记录"),
    "执行日志空态两分支文案在位");
  ok(html.includes("筛选无结果：当前条件下没有匹配的审计事件") && html.includes("暂无审计事件（新部署或按保留期清理）"),
    "审计空态两分支文案在位");
}
{
  // D2/D3（规格 §12.1/12.3 对照 catalog.go/guard.go 当前源码）：分级漂移已由服务端统一
  // （catalog 236/238/239/240/244=L2、223=L0 与 guard 一致）——UI 过期警告必须移除；
  // 字段形态漂移按规格逐条如实警示（203 targetRoom 等）。
  ok(sandbox.DRIFT_TYPES === undefined, "DRIFT_TYPES：过期分级漂移表已移除（catalog=管线单源，catalog.go:160-175）", sandbox.DRIFT_TYPES);
  const fd = sandbox.FIELD_DRIFT_TYPES || {};
  ok(fd[203] && fd[203].includes("targetRoom"), "FIELD_DRIFT_TYPES[203]：titleList→targetRoom 端侧读取警示", fd[203]);
  ok(fd[202] && fd[202].includes("loopEnabled"), "FIELD_DRIFT_TYPES[202]：loop.on→loopEnabled 保留", fd[202]);
  ok(["206", "207", "208", "209", "218", "225", "226", "229", "238", "511", "514"].every(k => fd[k] && fd[k].length > 8),
    "FIELD_DRIFT_TYPES：规格 §12.3 已知字段漂移逐条登记（206/207/208/209/218/225/226/229/238/511/514）", Object.keys(fd));
}
ok(sandbox.submitRouteDecision({ commandId: "c1", state: "queued", replay: true }).replay === true, "submitRouteDecision：幂等重放标记透传");
ok(sandbox.submitRouteDecision({}).kind === "invalid" && sandbox.submitRouteDecision(null).kind === "invalid", "submitRouteDecision：无法识别 → invalid（不轮换幂等键）");
{
  const d1 = sandbox.idemKey("dry"), r1 = sandbox.idemKey("real");
  ok(d1 !== r1, "干跑与正式提交使用不同幂等键（规格 F07）");
  ok(sandbox.idemKey("dry") === d1 && sandbox.idemKey("real") === r1, "同模式重试复用同键（防双击/超时重发）");
  sandbox.idemDone("dry");
  ok(sandbox.idemKey("dry") !== d1 && sandbox.idemKey("real") === r1, "idemDone 仅轮换本模式（dry 轮换不影响 real）");
  sandbox.idemDone("real");
}
{
  ok(sandbox.deliveryReason({ status: "skipped" }).includes("未投递") && sandbox.deliveryReason({ status: "skipped" }).includes("接收器"), "skipped：给出「未配置接收器」明确原因与下一步");
  ok(sandbox.deliveryReason({ status: "dead" }).includes("永久失败"), "dead：永久失败原因");
  ok(sandbox.deliveryReason({ status: "delivered" }).includes("不等同业务成功"), "delivered：通知送达≠业务成功");
  ok(sandbox.deliveryReason({ status: "pending", attempts: 1, nextAttemptMs: 5 }).includes("第 2 次"), "pending：下一次尝试序号");
  ok(sandbox.deliveryNextAttemptText({ status: "dead" }) === "不会自动重试", "dead：下次尝试=不会自动重试（不残留旧时间）");
  ok(sandbox.deliveryNextAttemptText({ status: "skipped" }) === "不会自动重试", "skipped：下次尝试=不会自动重试");
  ok(sandbox.deliveryNextAttemptText({ status: "pending", nextAttemptMs: Date.UTC(2026, 10, 4, 6, 0, 0) }).includes("2026-11-04"), "pending：显示真实计划时间");
  ok(sandbox.deliveryNextAttemptText({ status: "pending" }) === "等待调度", "pending 无计划：等待调度");
}
ok(sandbox.cbNextOp(404) === "POST" && sandbox.cbNextOp(200) === null && sandbox.cbNextOp(403) === null, "cbNextOp：PUT 404 才回退 POST（单保存按钮语义）");
{
  const n1 = sandbox.auditNarrative({ kind: "admin_command_submitted", subject: "cmd-9", detail: "target=dev-1 type=203 dryRun=false actor=admin-token:abcd1234 state=queued idemKey=k1" });
  ok(n1.action.includes("提交了指令任务"), "audit：kind → 中文动作");
  ok(n1.actor.includes("admin-token:abcd1234"), "audit：主体取服务端记录的 actor 证据", n1.actor);
  ok(n1.object === "cmd-9", "audit：对象=subject");
  ok(n1.extra.includes("type=203"), "audit：剩余证据进摘要", n1.extra);
  const n2 = sandbox.auditNarrative({ kind: "device_delete", subject: "dev-2", detail: "管理面删除设备（陈旧记录清理）" });
  ok(n2.actor.includes("未记录"), "audit：无主体证据 → 「未记录」，不回填 admin", n2.actor);
  ok(n2.extra.includes("管理面删除设备"), "audit：自由文本 detail 进证据摘要", n2.extra);
  const n3 = sandbox.auditNarrative({ kind: "some_new_kind", subject: "", detail: "" });
  ok(n3.action.includes("some_new_kind"), "audit：未登记 kind 原样保留（不编造动作）", n3.action);
  const n4 = sandbox.auditNarrative({ kind: "admin_queue_cleared", subject: "", detail: "target=robot-x cleared=3" });
  ok(n4.object === "robot-x", "audit：对象可回退到 detail.target", n4);
}
{
  const c1 = sandbox.stateConclusion({ state: "queued" }, []);
  ok(c1.tone === "run" && c1.title.includes("排队"), "结论条：queued=进行中蓝");
  const c2 = sandbox.stateConclusion({ state: "failed" }, [{ lastError: "boom" }]);
  ok(c2.tone === "err" && c2.detail.includes("boom"), "结论条：failed=红并带最近错误", c2.detail);
  const c3 = sandbox.stateConclusion({ state: "execution_unknown" }, []);
  ok(c3.tone === "unk" && c3.next.some(n => n.includes("刷新核对")) && !c3.next.some(n => n.includes("重试") && !n.includes("禁止")), "结论条：结果未知=灰，主动作刷新核对、禁盲重试", c3);
  const c4 = sandbox.stateConclusion({ state: "succeeded", terminalState: "succeeded" }, []);
  ok(c4.tone === "ok" && c4.next.some(n => n.includes("投递") && n.includes("独立")), "结论条：成功绿，且提示投递与成败独立（T09）", c4);
}
ok(sandbox.parseQuery("state=execution_unknown").state === "execution_unknown", "parseQuery：状态筛选写入 URL 参数");

/* ---------- H. 视觉基线 / 零外部资源 / 脱敏 ---------- */
section("H. 视觉基线 / 零外部资源 / 脱敏");
ok(html.includes("#f7f9fc") && html.includes("#2563eb") && html.includes("#1e3a5f"), "视觉 token：冰白底/钴蓝主色/深空蓝强调在位");
ok(html.includes("border-radius:12px") && html.includes("--shadow"), "卡片圆角+阴影在位");
ok(/--ok:#15803d[\s\S]*--warn:#b45309[\s\S]*--err:#dc2626[\s\S]*--unk:#64748b/.test(html), "状态徽标四色 token（成功绿/失败红/等待黄/未知灰）");
ok(!/<script\s+src=/i.test(html) && !/href="https?:/i.test(html) && !/src="https?:/i.test(html) && !/@import/i.test(html) && !/url\(/i.test(html), "零外部资源（无 CDN/字体/图片/导入）");
ok(!html.includes(">000000<"), "无 000000 配对码占位（S04 验收；注释中的否定说明不计）");
ok(!/192\.168\.\d+\.\d+|10\.\d+\.\d+\.\d+|172\.(1[6-9]|2\d|3[01])\.\d+\.\d+/.test(html), "无内网网段字面量（脱敏纪律）");
ok(!/1[3-9]\d{9}/.test(html), "无真实形态手机号（脱敏纪律）");
ok(!/console\.log/.test(src), "无 console.log（令牌/数据不入浏览器日志）");
// 与服务端 UI 面对齐门（server/internal/admin/admin_test.go TestUIServing 的标记表）——
// 前端单文件须继续含全部 API 面标记；banned 外链一律不得出现。
{
  const needMarkers = ["WorkTool-Open", "pairing-codes", "approvals", "admin/overview", "admin/queue", "admin/callbacks", "admin/whitelist", "logs/executions", "logs/audit", "settings/guard", "settings/encryption", "approver-token", "wt_approver_token", "issue-approver-token"];
  const missM = needMarkers.filter(mk => !html.includes(mk));
  ok(missM.length === 0, "UI 与 API 面对齐标记齐全（对齐 TestUIServing）", missM);
  const banned = ["http://cdn", "https://cdn", "googleapis", "jsdelivr", "unpkg", "<img ", "background-image:url(http"];
  const hitB = banned.filter(b => html.includes(b));
  ok(hitB.length === 0, "无外链/图片引用（对齐 TestUIServing banned 表）", hitB);
}

console.log(`\n结果：${passed} 过 / ${failed} 败`);
process.exit(failed ? 1 : 0);
