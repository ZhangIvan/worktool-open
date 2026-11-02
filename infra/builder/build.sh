#!/usr/bin/env bash
# WorkTool-Open Android 构建（T1.4）：在 S1 上用构建容器出 debug APK + sha256 清单
# 任务卡：docs/03-EXECUTION-PLAN.md §2 T1.4；约定：docs/00-MASTER-PLAN.md §5（S1 目录规范）
#
# 用法（在 S1 上）：
#   ./infra/builder/build.sh [仓库根目录]        # 缺省 = 本脚本所在仓库根
# 环境变量（可覆盖）：
#   IMAGE_NAME / IMAGE_TAG   构建镜像名（默认 worktool-open-builder:latest）
#   GRADLE_TASK              缺省 :app:assembleDebug
#   GRADLE_CACHE             宿主 Gradle 缓存目录（缺省 ~/.gradle；S1 建议
#                            /srv/worktool-open/builder/gradle-cache，见 infra/README.md）
#   SKIP_IMAGE_BUILD=1       镜像缺失时不自动 docker build（报错退出）
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${1:-$(cd -- "${SCRIPT_DIR}/../.." && pwd)}"
REPO_ROOT="$(cd -- "$REPO_ROOT" && pwd)"

IMAGE_NAME="${IMAGE_NAME:-worktool-open-builder}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
GRADLE_TASK="${GRADLE_TASK:-:app:assembleDebug}"
GRADLE_CACHE="${GRADLE_CACHE:-$HOME/.gradle}"

log() { printf '[build.sh] %s\n' "$*"; }
die() { printf '[build.sh] ERROR: %s\n' "$*" >&2; exit 1; }

# --- 前置检查 ---------------------------------------------------------------
command -v docker >/dev/null 2>&1 || die "docker 未安装（本脚本在 S1 宿主机上运行）"
docker info >/dev/null 2>&1 || die "docker daemon 不可达"

# android 壳工程存在性（T1.2 未落地时给出明确指引，而非让 gradle 报晦涩错误）
if [ ! -f "$REPO_ROOT/android/settings.gradle" ] && [ ! -f "$REPO_ROOT/android/settings.gradle.kts" ]; then
  die "$REPO_ROOT/android 下无 settings.gradle(.kts) —— T1.2 壳工程尚未落地，等 L1 车道交付后再跑"
fi

DIST_DIR="$REPO_ROOT/dist/app"
mkdir -p "$DIST_DIR" "$GRADLE_CACHE"

GIT_REV="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || true)"
[ -n "$GIT_REV" ] || GIT_REV="nogit"

# --- 构建镜像（幂等：存在即跳过；docker build 自带层缓存） -------------------
if ! docker image inspect "$IMAGE_NAME:$IMAGE_TAG" >/dev/null 2>&1; then
  if [ "${SKIP_IMAGE_BUILD:-0}" = "1" ]; then
    die "镜像 $IMAGE_NAME:$IMAGE_TAG 不存在且 SKIP_IMAGE_BUILD=1；请先执行：docker build -t $IMAGE_NAME:$IMAGE_TAG $SCRIPT_DIR"
  fi
  log "镜像 $IMAGE_NAME:$IMAGE_TAG 不存在，开始构建（首次需下载约 2-3 GB）…"
  docker build -t "$IMAGE_NAME:$IMAGE_TAG" "$SCRIPT_DIR"
fi

# --- 容器内构建 -------------------------------------------------------------
# 要点：
#   -u $(id -u):$(id -g)   以宿主用户身份运行（非 root），产物/缓存属主即宿主用户
#   -v 仓库:/workspace     源码挂载进容器，镜像本身不含源码
#   -v 缓存:/gradle-cache  Gradle 依赖缓存持久化在宿主，跨次构建复用
#   GRADLE_USER_HOME       指向挂载缓存；HOME=/tmp 兜底其余临时写入
log "运行 gradle $GRADLE_TASK（镜像 $IMAGE_NAME:$IMAGE_TAG，缓存 $GRADLE_CACHE）"
docker run --rm \
  -u "$(id -u):$(id -g)" \
  -e GRADLE_USER_HOME=/gradle-cache \
  -e HOME=/tmp \
  -v "$REPO_ROOT":/workspace:rw \
  -v "$GRADLE_CACHE":/gradle-cache:rw \
  -w /workspace/android \
  "$IMAGE_NAME:$IMAGE_TAG" \
  gradle --no-daemon "$GRADLE_TASK"

# --- 收集产物 + sha256 清单 --------------------------------------------------
found=0
while IFS= read -r -d '' apk; do
  base="$(basename "$apk")"
  # 文件名追加 git 短哈希（可追溯；无 git 时保持原名）
  dest="${DIST_DIR}/${base%.apk}-${GIT_REV}.apk"
  cp -f "$apk" "$dest"
  log "产物：$dest"
  found=1
done < <(find "$REPO_ROOT/android" -type f -name '*.apk' -path '*build/outputs/apk/*' -print0)

[ "$found" -eq 1 ] || die "gradle 成功但未在 android/**/build/outputs/apk/ 下发现 APK"

( cd "$DIST_DIR" && sha256sum ./*.apk > SHA256SUMS.txt )
log "清单已生成：$DIST_DIR/SHA256SUMS.txt"
printf '==== dist/app ====\n'
( cd "$DIST_DIR" && sha256sum -c SHA256SUMS.txt )
