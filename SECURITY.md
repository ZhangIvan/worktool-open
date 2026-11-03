# 安全策略（Security Policy）

- 版本：v1.0（2026-11-03，W-M4）；适用范围：本仓库全部代码与官方发布产物
- 安全基线全文：`docs/01-REQUIREMENTS.md` §2.1（SR-01~14）；门禁：`docs/04-AUDIT-CHECKLIST.md` §4（G1~G13）

## 支持版本

| 版本 | 状态 |
|------|------|
| v0.1.x（开发中） | ✅ 接受报告 |
| v0.1.0 之前的任何 commit | 尽力而为 |

## 报告漏洞

**请勿对安全问题开公开 issue / PR / 讨论。**

私有渠道：`security@` 邮箱见仓库维护者主页，或 GitHub **私密安全通告**（Security → Advisories → Report a vulnerability）。

报告时请附：影响版本/commit、复现步骤（报文/截图/日志）、影响面评估。我们承诺：

- 48 小时内确认收到；
- 修复期间可与你同步进展；发布修复前不公开细节；
- 修复发布后在 CHANGELOG 与安全通告中致谢（除非你要求匿名）。

**范围鼓励**：服务端鉴权绕过（HMAC/nonce/seq/tokenVersion）、SQL 注入、TLS 降级、
回调签名伪造、Android 端凭据存储（Keystore）弱点、更新机制未验签（未来版本）。

**范围外**：目标应用（企业微信）自身的漏洞；使用本项目违反平台条款导致的账号风控（见 README 合规与免责）；无法复现的"理论"问题；DoS。

## 安全设计要点（对使用者的承诺）

| 面 | 设计 | 规格落点 |
|----|------|----------|
| 传输 | 对外绑定强制 TLS（非 loopback 明文绑定启动即拒绝，SR-09）；端侧校验系统信任链 | protocol/spec.md §3 |
| 鉴权 | 配对码 6 位/TTL 10min/单次有效；deviceToken 仅存 SHA-256 hash；逐帧 HMAC-SHA256（seq 纳入防篡改）；nonce 双向 5min 去重；时钟 ±30s | spec §5.1/§5.2 |
| 加密 | 可选 AES-256-GCM 会话加密；会话密钥 X25519+HKDF-SHA256 派生；GCM nonce/AAD 冻结 | spec §5.3 |
| 端侧存储 | deviceToken 存 AndroidKeystore（SR-05）；不落明文 | spec §5.1.3 |
| 出网 | 外链 fileUrl 白名单默认拒收（G11）；默认零遥测零云依赖（G3）；静态数据保留期可配（SR-14） | spec §8 |
| 供应链 | 依赖钉死（go.sum / wrapper 8.7 / 版本目录）+ 依赖审计三线（govulncheck / Gradle / npm，Release Gate） | docs/04 §5.1 |
| 密钥 | 签名密钥与口令绝不入库（G13）；CI 以 base64 secret 落盘到 runner 临时目录，job 结束销毁 | docs/ops/SIGNING.md |

## 隐私

- 端侧数据（指令结果/日志/配置）默认仅发往**用户自建 server**；本地模式可完全离网。
- 服务端 SQLite/PG 数据归属部署者；默认配置无任何对外遥测、崩溃收集或统计上报。
- 断网隐私矩阵与出网白名单验证属发布门（docs/04 §5.1）。
