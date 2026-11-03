# infra/

构建与部署基础设施（全部容器化，脚本入库可审计、可复现）。

- `builder/`（T1.4）：Android 构建容器（JDK17 + Android cmdline-tools + Gradle 8.7，版本与 sha256 钉死）
- `compose.yaml` + `server.Dockerfile`（W-M4 / G-5）：server 一键部署（TLS 卷 + healthcheck + 可选 PG profile）
- 服务器：`friend@192.0.2.10`（Ubuntu，Docker 29.6.2 / Compose v5.3.1，目录 `/srv/worktool-open/`）

## compose 部署（server）

用法见 `compose.yaml` 文件头（三步：证书 → `docker compose -f infra/compose.yaml up -d --build` → `/healthz` 探活）。
可调项：`WT_PORT`（宿主端口）、`WT_CALLBACK_*`（回调分发）、`WT_GOPROXY`（受限网络模块代理）、
`--profile pg`（可选 Postgres；**服务端 PG 后端归 T3.x 未落地**，该 profile 只是预留位）。

**S1 实机冒烟实录（2026-11-03，W-M4）**：`config` 默认/pg 双形态校验通过；`up -d --build`
出镜像（19.3MB，多阶段 Go 构建）；容器 `healthy`（healthcheck=容器内 `wget --no-check-certificate /healthz`）；
`curl -sk https://127.0.0.1:18443/healthz → {"status":"ok"}`。两个坑（已在 compose 头部文档化）：
①非 root（uid 1000）容器读不了宿主属主的证书 → `chown 1000:1000 infra/certs`；
②compose 对非激活 profile 的服务同样做 `${:?}` 插值 → PG 密码用软插值，空值由 postgres
官方镜像启动即拒绝（fail-closed 不变）。

## S1 构建容器（T1.4）

登录：`ssh friend@192.0.2.10`（免密已打通，docs/00 §5）。

### 冒烟结论（2026-11-03，C-5/T1.4 关闭；证据 `local-private/evidence/2026-11-03/t1_4_smoke/`）

| 项 | 结果 |
|----|------|
| 磁盘 | `docker system prune -f` 回收 61.97GB → 90G 可用（此前 99%/31G） |
| `docker build infra/builder` | ✅ 首次 gradle zip 下载遇 curl exit 16（HTTP/2 瞬断）重试即过；**两处 zip 校验和实跑验证**：`/tmp/clt.zip: OK`、`/tmp/gradle.zip: OK` + `Gradle 8.7` 版本行 |
| 容器自检 | `docker run --rm worktool-open-builder:latest` → cmdline-tools 19.0 / Gradle 8.7 / adb 1.0.41 |
| 出包 | `bash infra/builder/build.sh` → `:app:assembleDebug` ✅ → `dist/app/app-debug-<rev>.apk` + `SHA256SUMS.txt`（`sha256sum -c` OK） |
| wrapper（C-13） | 容器内 `gradle wrapper --gradle-version 8.7` 生成 → 取回提交；干净容器 `./gradlew --version` 自下发行包 ✅ |

### S1 目录约定（docs/00 §5）

```
/srv/worktool-open/
├── src/                 # 本仓库源码树（git archive 或 clone，见下方注意）
├── builder/gradle-cache/  # Gradle 依赖缓存（挂载进容器，跨次构建复用）
└── server/              # W-M3R 实部署（lab TLS :8463，独立于 compose，勿动）
```

### 步骤（在 S1 上执行）

```bash
# 0) 源码上树（推荐 git clone；若从 Windows 端 git archive 推送，必须 -c core.autocrlf=false，
#    否则脚本被转成 CRLF，bash 报 "set: pipefail\r: invalid option name"——冒烟实录踩过）
git clone https://github.com/ZhangIvan/worktool-open.git /srv/worktool-open/src

# 1) 构建镜像（首次约 2-3 GB 下载；版本与 sha256 钉死在 Dockerfile ARG，可审计可复现）
docker build -t worktool-open-builder:latest /srv/worktool-open/src/infra/builder
docker run --rm worktool-open-builder:latest        # 自检

# 2) 出包（脚本自动跑容器内 assembleDebug → 产物落 dist/app/ → 生成并校验 SHA256SUMS.txt）
#    注意：build.sh 在 git 里是 100644（无执行位），用 bash 调用
GRADLE_CACHE=/srv/worktool-open/builder/gradle-cache \
  bash /srv/worktool-open/src/infra/builder/build.sh /srv/worktool-open/src

# 3)（可选）在本树生成 gradle wrapper（C-13 已入库一次；版本升级时重跑）
docker run --rm -u "$(id -u):$(id -g)" \
  -e GRADLE_USER_HOME=/gradle-cache -e HOME=/tmp \
  -v /srv/worktool-open/src:/workspace:rw \
  -v /srv/worktool-open/builder/gradle-cache:/gradle-cache:rw \
  -w /workspace/android worktool-open-builder:latest \
  gradle --no-daemon wrapper --gradle-version <版本>
```

要点：

- **镜像不含源码**：源码以 `-v` 挂载进 `/workspace`，产物写回挂载卷的 `dist/app/`（git ignored）。
- **非 root**：镜像默认用户 `builder`(uid 1000)；运行期 `-u $(id -u):$(id -g)` 保证产物属主为宿主用户。
- **`dist/app/` 产物名**：`<原apk名>-<git短哈希>.apk`；git archive 上树的树无 `.git` → 后缀 `nogit`
  （溯源以取证记录为准）。

### 版本固定（Dockerfile ARG，升级即改这里）

| 组件 | 版本 | 校验和（sha256） | 来源 |
|------|------|------------------|------|
| 基础镜像 | eclipse-temurin:17-jdk-jammy | —（tag 浮动，如需更强可审计性改 digest） | Docker Hub |
| cmdline-tools | 13114758（2026-11-02 核实最新） | `7ec96528…15317ee`（当日下载 zip 实算；2026-11-03 S1 实跑再验证 OK） | dl.google.com |
| platform-tools / platforms;android-34 / build-tools;34.0.0 | — | 经 sdkmanager（licenses 已接受） | dl.google.com |
| Gradle | 8.7 | `544c35d6…09b961d`（官方 .sha256；2026-11-03 S1 实跑再验证 OK） | services.gradle.org |
