# 发布签名密钥维护说明（Signing & Keystore）

- 版本：v1.0（2026-11-02）；负责人：维护者（ivans / zcode 线）
- **密钥文件永不入库**（`.gitignore` 已排除 `secrets/`、`*.jks`、`*.keystore`；CI 门禁 G13 强制拦截）

## 1. 现有密钥（首次生成于 2026-11-02）

| 项 | 值 |
|----|----|
| 文件 | `secrets/worktool-open.jks`（PKCS12，仓库根 `secrets/` 目录） |
| 别名 | `worktool-open` |
| 算法/长度 | RSA 2048（SHA256withRSA 签名） |
| 有效期 | 30 年（10950 天，至 ~2056） |
| 口令文件 | `secrets/keystore-pass.txt`（同目录，权限 600；store/key 同口令） |
| 证书指纹 | 见 `docs/ops/CERT-FINGERPRINT.txt`（SHA-256，公开安全，已入库；发布时粘贴至 Release 校验说明） |

> 本文档与指纹文件（`docs/ops/CERT-FINGERPRINT.txt`）已入库；`.jks` 与口令文件**绝不允许**出现在 git 追踪、截图、issue、日志或聊天记录中。

## 2. 日常使用

- 本地构建：通过 Gradle 环境变量注入，禁止写进任何受版本控制的文件：
  ```bash
  export WORKTOOL_STORE_FILE=secrets/worktool-open.jks
  export WORKTOOL_STORE_PASSWORD=$(cat secrets/keystore-pass.txt)
  export WORKTOOL_KEY_ALIAS=worktool-open
  export WORKTOOL_KEY_PASSWORD=$(cat secrets/keystore-pass.txt)
  ```
- CI 发布签名（后续）：口令放 GitHub Actions Secrets（仅 `zcode/*` 触发的 release 工作流），密钥文件按需上传为加密 secret 或仅在 S1 构建机本地签名后上传产物。
- 验签：`keytool -printcert -jarfile app.apk` 应与 `secrets/CERT-FINGERPRINT.txt` 一致；v2/v3 签名方案由 AGP 默认启用。

## 3. 备份与保管

- **丢失 = 无法升级安装**（Android 同包名升级必须同签名），因此：
  1. 离线备份两份：加密 U 盘 / 密码管理器附件（jks + 口令分开存放更佳）；
  2. 口令同时记录在密码管理器（勿只存明文文件）；
  3. 备份完成在本文件 §5 登记备份日期与位置（只写位置代号，不写内容）。
- `secrets/` 目录权限：仅当前用户可读（Windows 下属主限定，勿放网盘/共享目录）。

## 4. 轮换与事故处置

- **常规不轮换**（轮换=用户必须卸载重装）。
- 疑似泄露：①立即用旧签名发布一个"停止服务"最终版本；②生成新 keystore（新别名 `worktool-open-v2`）；③公告迁移（新旧指纹同时在 Release 说明公示过渡期）；④调查泄露路径并登记 AUD 事件。
- 密钥演进（如迁 Ed25519/硬件密钥）：仅在新应用包名或大版本迁移时评估。

## 5. 登记表

| 日期 | 事件 | 登记人 |
|------|------|--------|
| 2026-11-02 | 首次生成（RSA2048/30y）；离线备份：待登记（B-1 位置） | zcode |
