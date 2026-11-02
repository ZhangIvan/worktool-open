# protocol/

通讯协议规格（`spec.md` + JSON Schema + 测试夹具）。

- 原则：**文档 = 测试数据**（夹具直接被 android/server 单测引用，防止实现与文档漂移）
- v1 兼容信封与指令表还原自 `local-private/audit/02` §A；v2 增量（配对码/deviceToken/HMAC/ECDH-AES-GCM）见 `docs/02-ARCHITECTURE.md` §5
- 任务卡：T2.1、T3.4
