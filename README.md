# WorkTool-Open

> 开源的企业微信（WeCom）自动化机器人：**端侧无障碍执行 + 自托管调度后台**，数据默认不出你的内网。
>
> 本项目衍生自开源项目 [gallonyin/worktool](https://github.com/gallonyin/worktool)（Apache-2.0，仓库止于 v2.8.1 学习版），向原作者致谢。**非腾讯官方项目，与上游作者无关联。**

## 为什么有这个项目

上游开源版停更于 2023 年，其后的闭源版存在数据外送与可中间人攻击等缺陷（**内部审计资料由维护者本地保存，不入公开仓库**）。WorkTool-Open 以行为级重写方式重建全部能力，并做到：

- **数据主权**：默认零遥测、零云依赖；服务端你自己部署，机器人可完全离网运行（本地模式）
- **安全默认**：TLS 强制校验、无硬编码密钥、逐帧 HMAC 鉴权、出网白名单（`docs/01` §2.1 SR-01~11）
- **开放**：协议规格（`protocol/`）+ JSON Schema + 金标准测试向量全部开源（Apache-2.0），欢迎 issue / PR 共建

## 快速开始（三步）

> 当前版本 **v0.1.0 未发布**（开发中，里程碑见 [docs/00](docs/00-MASTER-PLAN.md)）。三步中：①服务端部署链已实证可用；③设备配对在产品 APK 上**尚未打通**（协议 v2 控制帧收尾中，任务 T3.4；真机证据口径见 [docs/05](docs/05-SUPPORT-MATRIX.md)——**未经真机执行回执的能力一律不宣称支持**）。

### ① 起 server（docker compose，需 TLS 证书）

```bash
git clone https://github.com/ZhangIvan/worktool-open.git && cd worktool-open
mkdir -p infra/certs
openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -subj "/CN=worktool-server.local" \
  -keyout infra/certs/server.key -out infra/certs/server.crt   # 内网自签一例；正式部署用自有证书
sudo chown -R 1000:1000 infra/certs && sudo chmod 600 infra/certs/server.key  # 容器内非 root（uid 1000）
docker compose -f infra/compose.yaml up -d --build
curl -sk https://localhost:8443/healthz        # → {"status":"ok"} 即就绪
```

细节与可调项（端口 `WT_PORT`、回调、可选 Postgres profile、受限网络 `WT_GOPROXY`）：[infra/compose.yaml](infra/compose.yaml) 文件头与 [infra/README.md](infra/README.md)。

### ② 装 APK（Android 端）

- 发布后：从 GitHub Releases 下载 APK（附 sha256 与签名证书指纹，指纹对照 [docs/ops/CERT-FINGERPRINT.txt](docs/ops/CERT-FINGERPRINT.txt)）；
- 现在：Actions 构建产物（`app-debug-apk` artifact），或用构建容器自建（`infra/builder/`，JDK17+AGP8.5+Gradle 8.7 已钉版带校验和）；
- 要求：Android 8.0+（minSdk 26）；开发验证基线为企微 5.0.x（支持范围逐版本见 [docs/05](docs/05-SUPPORT-MATRIX.md)）。

### ③ 配对（App ↔ 你的 server）

App 内进入配对面：填 server 地址（`https://<host>:8443`）+ 6 位配对码。配对码由服务端签发
（TTL 10 分钟、单次有效）；**管理 Web 面落地前**，配对码以服务端侧工具生成（与
`server/internal/api` 落库语义一致的引导脚本，见 `test/README.md`）。配对成功后设备与
server 建立 v2 安全会话（逐帧 HMAC + 可选 AES-256-GCM，规格见 [protocol/spec.md](protocol/spec.md)）。

> 本步在产品 APK 上的贯通受 T3.4（协议 v2 控制帧收尾）影响，当前真机状态如实登记于
> `docs/05` §备注；期间开发联调走 lab 变体（`assembleLabDebug`）。

## 仓库结构

| 目录 | 内容 | 状态 |
|------|------|------|
| `android/` | Android 端（Kotlin，`cn.worktool.open`；含 gradle wrapper） | M3 集成中 |
| `server/` | 调度后台（Go 单二进制，SQLite 默认 / PG 预留） | M3 集成中 |
| `admin/` | 管理 Web（React + TS） | M4 |
| `protocol/` | 通讯协议规格 + JSON Schema + 测试夹具（权威件） | ✅ v1.1.0 |
| `infra/` | 构建容器 / compose 部署 | ✅（S1 实证） |
| `test/` | 跨实现 E2E / 取证工具 | ✅ 入口统一仓库根 |
| `docs/` | 计划与规格（00~10）、第三方声明、签名维护说明 | ✅ |

## 安全

安全设计与威胁模型见 [SECURITY.md](SECURITY.md)（报告漏洞请走私有渠道，勿开公开 issue）。

## 文档

- [00 总体计划](docs/00-MASTER-PLAN.md) · [01 需求规格](docs/01-REQUIREMENTS.md) · [02 系统架构](docs/02-ARCHITECTURE.md) · [03 执行调度](docs/03-EXECUTION-PLAN.md) · [04 审计规程](docs/04-AUDIT-CHECKLIST.md) · [05 支持矩阵](docs/05-SUPPORT-MATRIX.md) · [06 服务端技术决策](docs/06-SERVER-TECH-DECISION.md) · [07 既有代码映射](docs/07-EXISTING-CODE-MAPPING.md)
- [CHANGELOG.md](CHANGELOG.md) · [CONTRIBUTING.md](CONTRIBUTING.md) · [docs/THIRD-PARTY-NOTICES.md](docs/THIRD-PARTY-NOTICES.md)

## 贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)：issue 关联需求编号（FR-xx/SR-xx）、PR 过 `docs/04` 质量门与 CI 门禁（G1~G13）、分支模型（`main` = 集成线，PR 流）。

## 合规与免责

- 本工具基于 Android 官方无障碍 API 实现自动化（技术路线开放、可审计，不修改/不注入/不破解目标应用）。**但"使用无障碍 API"是技术选择，不等于平台合规**：使用者须自行评估并遵守企业微信服务条款与所在地法律，账号风控/封禁风险由使用者自担（风险登记 R2）
- **产品实现零 hook**：产品代码不含 frida/Xposed/注入等动态篡改；逆向分析工具仅用于维护者内部的测试设备验证（与产品实现严格分离，门禁 G12）
- 禁止将本项目用于骚扰、欺诈、批量营销等任何侵害他人的用途

## License

Apache-2.0（含上游衍生义务，见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)）。第三方声明见 [docs/THIRD-PARTY-NOTICES.md](docs/THIRD-PARTY-NOTICES.md)。
