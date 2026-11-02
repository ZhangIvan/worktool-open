# infra/

构建与部署基础设施（全部容器化，脚本入库可审计、可复现）。

- `builder/`（T1.4）：S1 上的 Android 构建容器（JDK17 + Android cmdline-tools + Gradle），产物写 `dist/app/` + sha256
- `compose.yaml`（M4）：server（TLS 卷）+ 可选 Postgres + 可选内网 registry（S1:5000）
- 服务器：`friend@192.0.2.10`（Ubuntu，Docker 29.6.2，目录 `/srv/worktool-open/`）
