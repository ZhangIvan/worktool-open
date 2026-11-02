# WorkTool-Open Android 构建容器（T1.4，docs/03-EXECUTION-PLAN.md §2 / docs/02-ARCHITECTURE.md §7）
#
# 设计约束：
#   - 镜像内只含工具链，不嵌入任何仓库源码；源码与产物经卷挂载进出（build.sh 负责）。
#   - 版本与校验和固定（infra README "可审计、可复现" 承诺的落地面）。
#   - 非 root 运行；宿主产物属主由 build.sh 以 -u $(id -u):$(id -g) 保证。
#
# 版本核实记录（2026-11-02）：
#   - cmdline-tools 13114758 = dl.google.com 当前最新（HEAD 200 OK；更新的 13736356/14779862 均 404）
#     sha256 由本仓维护者当日下载计算（153 MB zip）。
#   - Gradle 8.7 sha256 取自官方 services.gradle.org/distributions/gradle-8.7-bin.zip.sha256。
# 升级方法：改下方 ARG（新版本号 + 新校验和），重新 docker build。

FROM eclipse-temurin:17-jdk-jammy

ARG CMDLINE_TOOLS_VERSION=13114758
ARG CMDLINE_TOOLS_SHA256=7ec965280a073311c339e571cd5de778b9975026cfcbe79f2b1cdcb1e15317ee
ARG GRADLE_VERSION=8.7
ARG GRADLE_SHA256=544c35d6bd849ae8a5ed0bcea39ba677dc40f49df7d1835561582da2009b961d
# 目标平台组件（android/ 规划 minSdk 24 / targetSdk 34+，android/README.md）；随 T1.2 实际值调整
ARG ANDROID_PLATFORM=android-34
ARG ANDROID_BUILD_TOOLS=34.0.0

ENV ANDROID_HOME=/opt/android-sdk \
    GRADLE_HOME=/opt/gradle \
    LANG=C.UTF-8
ENV PATH="${ANDROID_HOME}/cmdline-tools/latest/bin:${ANDROID_HOME}/platform-tools:${GRADLE_HOME}/bin:${PATH}"

# 基础工具
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl unzip; \
    rm -rf /var/lib/apt/lists/*

# Android cmdline-tools（校验和固定）→ 接受全部许可证 → platform-tools + 目标平台/构建工具
RUN set -eux; \
    curl -fsSLo /tmp/clt.zip \
      "https://dl.google.com/android/repository/commandlinetools-linux-${CMDLINE_TOOLS_VERSION}_latest.zip"; \
    echo "${CMDLINE_TOOLS_SHA256}  /tmp/clt.zip" | sha256sum -c -; \
    mkdir -p "${ANDROID_HOME}/cmdline-tools"; \
    unzip -q /tmp/clt.zip -d "${ANDROID_HOME}/cmdline-tools"; \
    mv "${ANDROID_HOME}/cmdline-tools/cmdline-tools" "${ANDROID_HOME}/cmdline-tools/latest"; \
    rm /tmp/clt.zip; \
    yes | sdkmanager --licenses > /dev/null; \
    sdkmanager --install "platform-tools" "platforms;${ANDROID_PLATFORM}" "build-tools;${ANDROID_BUILD_TOOLS}"; \
    sdkmanager --version

# Gradle（校验和固定，官方发行包）
RUN set -eux; \
    curl -fsSLo /tmp/gradle.zip \
      "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip"; \
    echo "${GRADLE_SHA256}  /tmp/gradle.zip" | sha256sum -c -; \
    unzip -q /tmp/gradle.zip -d /opt; \
    mv "/opt/gradle-${GRADLE_VERSION}" "${GRADLE_HOME}"; \
    rm /tmp/gradle.zip; \
    gradle --version | grep -F "Gradle ${GRADLE_VERSION}"

# 非 root 用户（uid 1000；运行期 build.sh 会以宿主 uid 覆盖，仍非 root）
RUN useradd --create-home --uid 1000 --shell /bin/bash builder

USER builder
WORKDIR /workspace

# 自检入口：docker run --rm <image> 即可验证工具链就绪
CMD ["bash", "-c", "echo 'worktool-open builder ready'; sdkmanager --version | head -1; gradle --version | grep -F Gradle; adb --version | head -1"]
