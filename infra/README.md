# infra/

构建与部署基础设施（全部容器化，脚本入库可审计、可复现）。

- `builder/`（T1.4）：S1 上的 Android 构建容器（JDK17 + Android cmdline-tools + Gradle），产物写 `dist/app/` + sha256
- `compose.yaml`（M4）：server（TLS 卷）+ 可选 Postgres + 可选内网 registry（S1:5000）
- 服务器：`friend@192.0.2.10`（Ubuntu，Docker 29.6.2，目录 `/srv/worktool-open/`）

## S1 构建容器用法（T1.4）

登录：`ssh friend@192.0.2.10`（免密已打通，docs/00 §5）。

### 远端预检结果（2026-11-02，尽力而为项）

```
$ ssh -o BatchMode=yes -o ConnectTimeout=8 friend@192.0.2.10 'docker --version; df -h /srv 2>/dev/null || df -h /'
Docker version 29.6.2, build dfc4efb          # OK
Docker Compose version v5.3.1                 # OK（docs/00 §5 "compose 版本待确认"已补实）
/dev/nvme0n1p2  1.9T  1.8T   31G  99%  /      # ⚠️ 仅剩 31G
/srv/worktool-open                             # 尚未创建（首次使用时 mkdir）
```

> ⚠️ **磁盘风险**：根分区 99% 已用、仅剩 31G。构建镜像约 2-3 GB + Gradle 缓存首跑约 2-4 GB，可跑但偏紧；
> 首次构建前建议清理（`docker system df` 评估 → `docker system prune`）并留意 `~/.gradle` 体积。

### S1 目录约定（docs/00 §5）

```
/srv/worktool-open/
├── src/                 # 本仓库克隆（git clone <repo-url> src）
├── builder/
│   └── gradle-cache/    # Gradle 依赖缓存（挂载进容器，跨次构建复用）
├── server/  artifacts/  scripts/  logs/    # M4 起用
```

### 步骤（在 S1 上执行）

```bash
# 0) 目录准备（一次性）
sudo mkdir -p /srv/worktool-open/{src,builder/gradle-cache} && sudo chown -R "$USER" /srv/worktool-open
git clone <repo-url> /srv/worktool-open/src

# 1) 构建镜像（首次约 2-3 GB 下载；版本与 sha256 均固定在 Dockerfile，可审计可复现）
docker build -t worktool-open-builder:latest /srv/worktool-open/src/infra/builder
#    自检（可选）：docker run --rm worktool-open-builder:latest

# 2) 出包（推荐：脚本一键完成——容器内 assembleDebug → 产物落 dist/app/ → 生成 SHA256SUMS.txt）
GRADLE_CACHE=/srv/worktool-open/builder/gradle-cache \
  /srv/worktool-open/src/infra/builder/build.sh /srv/worktool-open/src

# 3) 校验产物
cd /srv/worktool-open/src/dist/app && sha256sum -c SHA256SUMS.txt
```

`build.sh` 等价的手动 docker run（脚本内部即此命令，便于单独排障）：

```bash
docker run --rm \
  -u "$(id -u):$(id -g)" \
  -e GRADLE_USER_HOME=/gradle-cache -e HOME=/tmp \
  -v /srv/worktool-open/src:/workspace:rw \
  -v /srv/worktool-open/builder/gradle-cache:/gradle-cache:rw \
  -w /workspace/android \
  worktool-open-builder:latest \
  gradle --no-daemon :app:assembleDebug
```

要点：

- **镜像不含源码**：源码以 `-v` 挂载进 `/workspace`，产物写回挂载卷的 `dist/app/`（git ignored）。
- **非 root**：镜像默认用户 `builder`(uid 1000)；运行期 `-u $(id -u):$(id -g)` 保证产物属主为宿主用户。
- **T1.2 前置**：`android/settings.gradle(.kts)` 未落地时 `build.sh` 会明确报错退出（等 L1 车道壳工程）。
- **本轮范围（2026-11-02）**：仅交付脚本与预检，按任务卡要求未拉镜像、未跑构建——留待下轮执行上节步骤。

### 版本固定（Dockerfile ARG，升级即改这里）

| 组件 | 版本 | 校验和（sha256） | 来源 |
|------|------|------------------|------|
| 基础镜像 | eclipse-temurin:17-jdk-jammy | —（tag 浮动，如需更强可审计性改 digest） | Docker Hub |
| cmdline-tools | 13114758（2026-11-02 核实最新） | `7ec96528…15317ee`（当日下载 zip 实算） | dl.google.com |
| platform-tools / platforms;android-34 / build-tools;34.0.0 | — | 经 sdkmanager（licenses 已接受） | dl.google.com |
| Gradle | 8.7 | `544c35d6…09b961d`（官方 .sha256） | services.gradle.org |
