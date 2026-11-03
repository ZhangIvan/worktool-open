#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""跨实现一致性 E2E（test/e2e 车道）：python 规范客户端（cryptography 库）打真实 Go 服务。

权威件 = protocol/（spec/schema/夹具/状态机，ADR-08）。本脚本是一个**独立第三方实现**
（不复用 Go/Kotlin 代码）：配对（Ed25519 签名公钥 + X25519 临时公钥，cryptography 库）、
逐帧 HMAC-SHA256（spec §5.2.2，AUD-03 C-9 输入式 commandId|eventId|seq|ts|nonce|payloadDigest）、
JCS(RFC 8785) payloadDigest、AES-256-GCM 会话加密（spec §5.3，X25519+HKDF-SHA256）全部
按 protocol/spec.md 独立实现，与 Go 服务端（server/internal/wire）做系统级一致性互操作。

链路（对齐车道指令）：
  ① 规范客户端：配对 → hello/hello_ack（验服务端签名）→ SEND_TEXT(203) 干跑指令（FR-54，
     服务端代行、绝不下发设备）→ 收结果（RAW_CONFIRM）→ 真实下发链（GCM 解密互操作）→
     结果回执 → 墓碑重放同 eventId 幂等（§5.4.3）→ 篡改 seq 必拒（C-9 → 4401）→
     （时钟注入）ACK TTL 到期 E17 对账路径存在性断言（禁止超时定性成败）。
  ①' T3.4 控制帧冻结三方断言（spec §5.7）：hello/hello_ack/result 三关键帧 +
     901~904 全部夹具正反例过 jsonschema（python 第三方消费面）；服务端实际产出
     hello_ack **字节级比对**冻结基准 fixtures/session-hello-ack.go-actual.json
     （紧凑形/字段序/掩蔽动态字段后 JCS 规范形三层）；python 生产面出帧自校验
     （hello 过 session-hello.schema、结果回执过 exec-callback.schema）；
     dispatch 载荷携带 epoch；真实签名乱序 seq → 903 RESYNC 帧后 4405 关闭；
     execution_unknown 重连 → 904 journal_replay 请求 → 持久证据补报 → E12 离出
     + 墓碑 + 补报幂等。
  ② apifox 兼容面冒烟：/openapi/v1 203/206 干跑全链 + RAW_CONFIRM 查询（含 404）+
     幂等键正/负例（§5.6.1）+ 60QPM 限流负例（NFR §2.2）。
  ③ 服务清理：进程退出断言。

测试引导（bootstrap，非协议行为）——admin HTTP 面（POST /api/v1/admin/pairing-codes）
已交付；本脚本为保持自包含/不依赖 admin token 仍选择直写 SQLite（与 Go 侧写入逐字段一致，分别为
api/pair.go:165 CreatePairingCode、api/openapi_registry.go:140 IssueOpenAPIToken、
api/openapi_registry.go:178 resolveTarget 所需的 robot_id 绑定）：
  - pairing_codes 行（6 位码 + SHA-256 hash + 10min TTL，单次有效由服务端原子消费）
  - api_tokens 行（Bearer token 仅存 SHA-256 hash）
  - devices.robot_id 绑定（已配对设备 ↔ 旧面 robotId 寻址）

时钟注入说明：Go 侧 Service.Now 为进程内可注入（scheduler/pipeline.go:57），子进程二进制
无外部注入口；本脚本以**持久层等价注入**驱动同一代码路径——把 commands.ack_deadline
改写到过去，RunAckTTLScanner（cmd/server/main.go:95，10s 周期）→ ScanAckTTL → E17
（pipeline.go:713）照常触发。断言口径不变：只允许 execution_unknown（对账登记），
禁止超时定性 succeeded/failed（state-policy.json#dispatch.ack_ttl_expiry）。

退出码：0 = 全部断言通过；1 = 存在失败断言；2 = 环境错误（go/python/依赖/启动失败）。
所有断言汇总输出到 stdout 尾部 SUMMARY 行。

用法：python test/e2e/run_cross_e2e.py [--repo-root PATH] [--keep] [--verbose]
"""

import argparse
import hashlib
import hmac as hmac_lib
import http.client
import json
import os
import secrets
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid as uuid_lib
from datetime import datetime, timezone
from pathlib import Path

try:
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
    from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    from cryptography.hazmat.primitives.kdf.hkdf import HKDF
except ImportError as e:  # pragma: no cover
    print(f"[env] cryptography 库不可用: {e}", file=sys.stderr)
    sys.exit(2)

try:
    import websocket  # websocket-client：仅作 RFC 6455 传输层，协议逻辑全部本脚本实现
except ImportError as e:  # pragma: no cover
    print(f"[env] websocket-client 不可用: {e}", file=sys.stderr)
    sys.exit(2)

try:
    import jsonschema  # T3.4：控制帧冻结 schema 的第三方（python）消费面
except ImportError as e:  # pragma: no cover
    print(f"[env] jsonschema 库不可用: {e}", file=sys.stderr)
    sys.exit(2)

# ---- 冻结常量（protocol/spec.md / state-policy.json 权威值） ----
EMPTY_SHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
HKDF_INFO = b"worktool-open/v2/session"  # spec §5.3 冻结
CLOSE_AUTH_FAILURE = 4401  # spec §5.2.5
ACK_TTL_SECONDS = 300      # state-policy.json#dispatch.ack_ttl_seconds
TOMBSTONE_MIN_DAYS = 7     # state-policy.json#tombstone.min_retention_days
HEARTBEAT_SECS = 15
SESSION_TIMEOUT_SECS = 45
ROTATION_DAYS = 30

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")


# =====================================================================
# 断言账本
# =====================================================================
class Checks:
    def __init__(self):
        self.items = []  # (name, status, detail) status: PASS/FAIL/SKIP

    def check(self, name, cond, detail=""):
        status = "PASS" if cond else "FAIL"
        self.items.append((name, status, detail))
        mark = "✓" if cond else "✗"
        print(f"  [{status}] {name}" + (f" — {detail}" if detail and not cond else ""))
        return cond

    def skip(self, name, reason):
        self.items.append((name, "SKIP", reason))
        print(f"  [SKIP] {name} — {reason}")

    def summary_line(self):
        total = len(self.items)
        passed = sum(1 for _, s, _ in self.items if s == "PASS")
        failed = sum(1 for _, s, _ in self.items if s == "FAIL")
        skipped = sum(1 for _, s, _ in self.items if s == "SKIP")
        result = "PASS" if failed == 0 else "FAIL"
        return (f"SUMMARY: total={total} pass={passed} fail={failed} "
                f"skipped={skipped} result={result}")

    def print_summary(self):
        print("\n==== CROSS-E2E 断言汇总 ====")
        for i, (name, status, detail) in enumerate(self.items, 1):
            line = f"[{i}/{len(self.items)}] {status} {name}"
            if status == "FAIL" and detail:
                line += f" — {detail}"
            print(line)
        print(self.summary_line())


def now_ms() -> int:
    return int(time.time() * 1000)


def sha256_hex(s: str) -> str:
    return hashlib.sha256(s.encode("utf-8")).hexdigest()


def new_uuid() -> str:
    return str(uuid_lib.uuid4())


# =====================================================================
# RFC 8785（JCS）规范化 —— python 独立实现（对照 Go wire/jcs.go 互操作）
# 已知边界：数值仅支持安全整数（本测试全部载荷为整数/字符串/数组/对象）；
# 键按 UTF-16 码元序；字符串按 JSON.stringify 转义规则。
# =====================================================================
def _canon_string(s: str) -> str:
    out = ['"']
    for ch in s:
        if ch == '"':
            out.append('\\"')
        elif ch == "\\":
            out.append("\\\\")
        elif ch == "\b":
            out.append("\\b")
        elif ch == "\f":
            out.append("\\f")
        elif ch == "\n":
            out.append("\\n")
        elif ch == "\r":
            out.append("\\r")
        elif ch == "\t":
            out.append("\\t")
        elif ord(ch) < 0x20:
            out.append("\\u%04x" % ord(ch))
        else:
            out.append(ch)
    out.append('"')
    return "".join(out)


def _canon_value(v) -> str:
    if v is None:
        return "null"
    if v is True:
        return "true"
    if v is False:
        return "false"
    if isinstance(v, str):
        return _canon_string(v)
    if isinstance(v, int):  # bool 是 int 子类，已在上面拦截
        if abs(v) >= 2 ** 53:
            raise ValueError(f"JCS: 整数超出 IEEE-754 安全整数域: {v}")
        return str(v)
    if isinstance(v, float):
        raise ValueError("JCS: 测试载荷不支持浮点（RFC 8785 ES 数值序列化未实现）")
    if isinstance(v, list):
        return "[" + ",".join(_canon_value(e) for e in v) + "]"
    if isinstance(v, dict):
        keys = sorted(v.keys(), key=lambda k: k.encode("utf-16-be"))
        return "{" + ",".join(f"{_canon_string(k)}:{_canon_value(v[k])}" for k in keys) + "}"
    raise TypeError(f"JCS: 不支持的类型 {type(v)}")


def canonical_json(v) -> str:
    return _canon_value(v)


def payload_digest(encrypt_type: int, list_obj, encrypted_list: str) -> str:
    """spec §5.2.2：encryptType=0 → SHA-256(JCS(list))（空/缺失=空字节串哈希）；
    encryptType=2 → SHA-256(encryptedList 字符串原样 UTF-8)。"""
    if encrypt_type == 2:
        return hashlib.sha256(encrypted_list.encode("utf-8")).hexdigest()
    if not list_obj:
        return EMPTY_SHA256
    return hashlib.sha256(canonical_json(list_obj).encode("utf-8")).hexdigest()


def frame_hmac(token_hex: str, command_id: str, event_id: str, seq: int,
               ts: int, nonce: str, digest: str) -> str:
    """spec §5.2.2（C-9 输入式）：key=deviceToken 原始字节，
    msg=UTF-8(commandId|eventId|seq|ts|nonce|payloadDigest)。"""
    key = bytes.fromhex(token_hex)
    msg = f"{command_id}|{event_id}|{seq}|{ts}|{nonce}|{digest}".encode("utf-8")
    return hmac_lib.new(key, msg, hashlib.sha256).hexdigest()


def build_meta(token_hex: str, command_id: str, seq: int, digest: str,
               token_version: int = 1, ts=None, nonce=None, hmac_seq=None):
    """构造 v2 逐帧鉴权 meta。hmac_seq ≠ None 时 HMAC 按 hmac_seq 计算、
    meta.seq 声明为 seq —— 用于「篡改 seq 必拒」反例（fixtures/hmac-vector.json negative）。"""
    ts = now_ms() if ts is None else ts
    nonce = secrets.token_hex(16) if nonce is None else nonce
    event_id = new_uuid()
    mac = frame_hmac(token_hex, command_id, event_id,
                     seq if hmac_seq is None else hmac_seq, ts, nonce, digest)
    return {
        "ver": 2, "ts": ts, "nonce": nonce, "hmac": mac, "seq": seq,
        "eventId": event_id, "commandId": command_id, "tokenVersion": token_version,
    }


def build_message_id(ts_ms: int) -> str:
    """spec §4.1：yyyy-MM-dd#HH:mm:ss#<UUID>（上游还原格式）。"""
    t = datetime.fromtimestamp(ts_ms / 1000, tz=timezone.utc)
    return t.strftime("%Y-%m-%d#%H:%M:%S") + "#" + new_uuid()


# =====================================================================
# HTTP 最小客户端（loopback，json）
# =====================================================================
def http_json(host, port, method, path, body=None, headers=None, timeout=15):
    conn = http.client.HTTPConnection(host, port, timeout=timeout)
    try:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8") if body is not None else None
        hdrs = {"Accept": "application/json"}
        if data is not None:
            hdrs["Content-Type"] = "application/json"
        if headers:
            hdrs.update(headers)
        conn.request(method, path, body=data, headers=hdrs)
        resp = conn.getresponse()
        raw = resp.read()
        try:
            parsed = json.loads(raw.decode("utf-8")) if raw else None
        except (ValueError, UnicodeDecodeError):
            parsed = raw.decode("utf-8", "replace")
        return resp.status, parsed
    finally:
        conn.close()


def wait_healthz(host, port, timeout_s=30):
    deadline = time.time() + timeout_s
    last = None
    while time.time() < deadline:
        try:
            status, body = http_json(host, port, "GET", "/healthz", timeout=3)
            if status == 200 and isinstance(body, dict) and body.get("status") == "ok":
                return True
            last = f"status={status} body={body}"
        except OSError as e:
            last = str(e)
        time.sleep(0.4)
    print(f"[env] /healthz 未就绪: {last}")
    return False


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


# =====================================================================
# SQLite bootstrap（控制台/admin 面占位期的等价写入；见模块 docstring）
# =====================================================================
def db_connect(db_path):
    con = sqlite3.connect(db_path, timeout=20)
    return con


def seed_pairing_code(db_path, code):
    con = db_connect(db_path)
    try:
        con.execute(
            "INSERT INTO pairing_codes(code_hash, expires_at, created_at) VALUES (?,?,?)",
            (sha256_hex(code), now_ms() + 10 * 60 * 1000, now_ms()))
        con.commit()
    finally:
        con.close()


def seed_api_token(db_path, raw_token, robot_id):
    con = db_connect(db_path)
    try:
        con.execute(
            "INSERT INTO api_tokens(token_hash, robot_id, tenant, status, created_ms) "
            "VALUES (?,?,?,'active',?)",
            (sha256_hex(raw_token), robot_id, "default", now_ms()))
        con.commit()
    finally:
        con.close()


def bind_robot_to_device(db_path, device_id, robot_id):
    con = db_connect(db_path)
    try:
        con.execute("UPDATE devices SET robot_id=? WHERE device_id=?", (robot_id, device_id))
        con.commit()
    finally:
        con.close()


def db_query_one(db_path, sql, args=()):
    con = db_connect(db_path)
    try:
        row = con.execute(sql, args).fetchone()
        return row
    finally:
        con.close()


def db_exec(db_path, sql, args=()):
    con = db_connect(db_path)
    try:
        con.execute(sql, args)
        con.commit()
    finally:
        con.close()


# =====================================================================
# python 规范客户端（v2 安全会话）
# =====================================================================
class SpecClient:
    def __init__(self, host, port, db_path, checks: Checks, repo: Path):
        self.host, self.port, self.db_path, self.checks = host, port, db_path, checks
        self.repo = repo  # protocol/ 权威件根（生产面出帧 schema 自校验用）
        self.base = f"http://{host}:{port}"
        self.ed_priv = Ed25519PrivateKey.generate()
        self.x_priv = X25519PrivateKey.generate()
        self.device_id = "e2e-dev-" + secrets.token_hex(4)
        self.token_hex = None
        self.token_version = 1
        self.ws = None
        self._exec_items = {}    # event_id → 已发结果回执 item（幂等重放逐字节复用）
        self.in_seq = 0          # 设备→服务端 seq（hello=1 起严格递增）
        self.last_server_seq = 0  # 已收到的最高服务端→设备 seq

    # ---- 密钥材料 ----
    def ed_pub_hex(self) -> str:
        return self.ed_priv.public_key().public_bytes(
            serialization.Encoding.Raw, serialization.PublicFormat.Raw).hex()

    def x_pub_hex(self) -> str:
        return self.x_priv.public_key().public_bytes(
            serialization.Encoding.Raw, serialization.PublicFormat.Raw).hex()

    def session_key(self, server_x25519_pub_hex: str, session_id: str) -> bytes:
        shared = self.x_priv.exchange(
            X25519PublicKey.from_public_bytes(bytes.fromhex(server_x25519_pub_hex)))
        return HKDF(algorithm=hashes.SHA256(), length=32,
                    salt=session_id.encode("utf-8"), info=HKDF_INFO).derive(shared)

    # ---- REST：配对 / 会话 ----
    def post_pair(self, code):
        body = {
            "pairingCode": code,
            "deviceId": self.device_id,
            "deviceName": "cross-e2e-py",
            "ed25519PublicKey": self.ed_pub_hex(),
            "x25519EphemeralPublicKey": self.x_pub_hex(),
        }
        return http_json(self.host, self.port, "POST", "/api/v2/pair", body)

    def post_device_session(self):
        return http_json(self.host, self.port, "POST", "/api/v2/device-sessions",
                         {"deviceId": self.device_id})

    # ---- WS ----
    def dial(self, ws_path):
        url = f"ws://{self.host}:{self.port}{ws_path}"
        self.ws = websocket.create_connection(url, timeout=15)
        return self.ws

    def send_json(self, obj):
        self.ws.send(json.dumps(obj, ensure_ascii=False))

    def recv_json(self, timeout=10):
        self.ws.settimeout(timeout)
        data = self.ws.recv()
        if not isinstance(data, str):
            data = data.decode("utf-8", "replace")
        return json.loads(data)

    def recv_raw_json(self, timeout=10):
        """读一帧返回 (原始文本, 解析对象)。字节级比对输入 = 实收原文
        （严格 UTF-8 解码：字节损坏即抛错，不做 replace 掩盖）。"""
        self.ws.settimeout(timeout)
        data = self.ws.recv()
        raw = data.decode("utf-8") if isinstance(data, bytes) else data
        return raw, json.loads(raw)

    def server_close(self, timeout=8):
        """读至 OP_CLOSE（control=False 透传控制帧），返回 (关闭码, 原因文本)。

        spec §5.2.5：拒帧以 WS 关闭码承载（4401=鉴权失败等）；原因文本为人读信息。
        """
        deadline = time.time() + timeout
        while time.time() < deadline:
            self.ws.settimeout(max(0.5, deadline - time.time()))
            try:
                opcode, data = self.ws.recv_data(control_frame=False)
            except websocket.WebSocketTimeoutException:
                return None, "timeout: 未收到关闭帧"
            except Exception as e:  # 连接已断且未读到 close 帧
                return getattr(self.ws, "close_status", None), f"recv 异常: {e}"
            if opcode == websocket.ABNF.OPCODE_CLOSE:
                if len(data) >= 2:
                    code = int.from_bytes(data[:2], "big")
                    reason = data[2:].decode("utf-8", "replace")
                    return code, reason
                return None, "close 帧无载荷"
        return None, "timeout: 未收到关闭帧"

    def send_hello(self, last_epoch=0, last_received_seq=0, label="hello"):
        digest = EMPTY_SHA256  # hello 为空载荷帧（spec §5.4.1）
        meta = build_meta(self.token_hex, "", 1, digest, self.token_version)
        hello = {
            "type": "hello",
            "deviceId": self.device_id,
            "tokenVersion": self.token_version,
            "lastEpoch": last_epoch,
            "lastReceivedSeq": last_received_seq,
            "capabilities": {"clientVersion": "cross-e2e-py/1.0",
                             "encryptTypeMax": 2, "commands": [203]},
            "meta": meta,
        }
        # 生产面自校验（三方一致之 python 分量）：出帧即过冻结 schema（spec §5.7）
        err = validate_schema(hello, self.repo / "protocol" / "schema"
                              / "session-hello.schema.json")
        self.checks.check(f"{label}.sent_schema_conform", err is None, err or "")
        self.in_seq = 1
        self.send_json(hello)
        return hello

    def verify_server_frame_meta(self, name, meta, expect_digest):
        """验服务端出站帧签名（双向逐帧鉴权一致性）。"""
        ok_hmac = frame_hmac(self.token_hex, meta.get("commandId", ""), meta["eventId"],
                             meta["seq"], meta["ts"], meta["nonce"], expect_digest) == meta["hmac"]
        self.checks.check(f"{name}.server_hmac", ok_hmac,
                          f"服务端帧 HMAC 验签失败: {meta}")
        return ok_hmac

    def next_meta(self, command_id: str, digest: str, seq=None, hmac_seq=None):
        seq = self.in_seq + 1 if seq is None else seq
        meta = build_meta(self.token_hex, command_id, seq, digest,
                          self.token_version, hmac_seq=hmac_seq)
        self.in_seq = seq
        return meta

    def send_exec_result(self, command_id: str, event_id: str, cmd_type: int,
                         error_code: int, target_room: str, seq: int,
                         label="execresult"):
        """socketType=3 结果回执（ExecCallbackBean，spec §4.3）。event_id 复用时
        载荷必须逐值一致（同 payloadDigest → §5.4.3 幂等回执）——item 按 event_id
        缓存复用（runTime 等时变字段不得在重放时重新生成：跨秒边界 → digest 漂移
        → 服务端按「同 ID 异内容」4405 冲突关闭，§5.4.3/v2.go:443）。"""
        cached = self._exec_items.get(event_id)
        if cached is not None:
            item = cached  # 幂等重放：逐字节同载荷（同 payloadDigest）
        else:
            item = {
                "type": cmd_type,
                "errorCode": error_code,
                "errorReason": "" if error_code == 0 else "e2e-injected-failure",
                "runTime": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S"),
                "timeCost": 831,
                "successList": [target_room] if error_code == 0 else [],
                "failList": [] if error_code == 0 else [target_room],
                "metaJson": json.dumps({"dispatchId": command_id}),
            }
            self._exec_items[event_id] = item
        # 生产面自校验：result/ack 载荷过冻结 schema（exec-callback，spec §5.7.2）
        err = validate_schema(item, self.repo / "protocol" / "schema"
                              / "exec-callback.schema.json")
        self.checks.check(f"{label}.sent_schema_conform", err is None, err or "")
        digest = payload_digest(0, [item], "")
        meta = build_meta(self.token_hex, command_id, seq, digest, self.token_version)
        meta["eventId"] = event_id  # 重放：同 eventId
        meta["hmac"] = frame_hmac(self.token_hex, command_id, event_id,
                                  seq, meta["ts"], meta["nonce"], digest)
        frame = {
            "socketType": 3,
            "messageId": build_message_id(now_ms()),
            "meta": meta,
            "apiSend": 1,
            "encryptType": 0,
            "list": [item],
        }
        self.in_seq = seq
        self.send_json(frame)
        return frame

    def decrypt_frame_list(self, frame, session_key):
        """encryptType=2 → AES-256-GCM 解密（spec §5.3：nonce=SHA-256(eventId)[0:12]，
        AAD=ver=2|seq=…|eventId=…|ts=…，密文=hex(密文||tag)）。"""
        meta = frame["meta"]
        nonce = hashlib.sha256(meta["eventId"].encode()).digest()[:12]
        aad = f"ver=2|seq={meta['seq']}|eventId={meta['eventId']}|ts={meta['ts']}".encode()
        plain = AESGCM(session_key).decrypt(nonce, bytes.fromhex(frame["encryptedList"]), aad)
        return json.loads(plain.decode("utf-8"))

    def recv_command_frame(self, session_key, want_command_id, timeout=15):
        """读 WS 直到收到 meta.commandId 匹配的下发指令帧（中途控制帧跳过）。"""
        deadline = time.time() + timeout
        while time.time() < deadline:
            self.ws.settimeout(max(1, deadline - time.time()))
            try:
                frame = self.recv_json(timeout=max(1, deadline - time.time()))
            except websocket.WebSocketTimeoutException:
                break
            if not isinstance(frame, dict) or "meta" not in frame:
                continue  # hello_ack / 控制帧
            if frame.get("meta", {}).get("commandId") != want_command_id:
                continue
            self.last_server_seq = max(self.last_server_seq, frame["meta"]["seq"])
            return frame
        return None

    def expect_silence(self, seconds):
        """断言 seconds 内无任何服务端帧（FR-54：干跑绝不下发设备）。"""
        deadline = time.time() + seconds
        while time.time() < deadline:
            self.ws.settimeout(max(0.2, deadline - time.time()))
            try:
                self.ws.recv()
                return False
            except websocket.WebSocketTimeoutException:
                return True
            except Exception:
                return False  # 连接异常 ≠ 静默
        return True


# =====================================================================
# Phase 0.5：控制帧冻结夹具三方断言（T3.4，spec §5.7）
# python（独立第三方）以 jsonschema 直接消费 protocol/ 机器件：
# 正例全部通过、反例全部被拒——与 Go wire/control_test.go、Kotlin
# ControlFrameTest 构成三实现一致性（Go 产 / python 收 / Kotlin 收）。
# =====================================================================
CONTROL_FIXTURES_POSITIVE = [
    ("session-hello-ack.go-actual.json", "session-hello-ack.schema.json"),
    ("control-payload.901-rotate.json", "control-payload.schema.json"),
    ("control-payload.902-revoked.json", "control-payload.schema.json"),
    ("control-payload.903-resync.json", "control-payload.schema.json"),
    ("control-payload.904-replay.json", "control-payload.schema.json"),
    ("control-payload.904-request.json", "control-payload.schema.json"),
]
CONTROL_FIXTURES_NEGATIVE = [
    ("session-hello-ack.bad-frozen.json", "session-hello-ack.schema.json"),
    ("session-hello-ack.envelope-wrapped.json", "session-hello-ack.schema.json"),
    ("control-payload.901-bad-token.json", "control-payload.schema.json"),
    ("control-payload.903-extra-field.json", "control-payload.schema.json"),
    ("control-payload.904-empty.json", "control-payload.schema.json"),
]


def validate_schema(instance, schema_path: Path):
    schema = json.loads(schema_path.read_text(encoding="utf-8"))
    try:
        jsonschema.Draft7Validator(schema).validate(instance)
        return None
    except jsonschema.ValidationError as e:
        return str(e.message)[:120]


def phase0_control_frame_fixtures(repo: Path, ck: Checks):
    print("\n== Phase 0.5: 控制帧冻结夹具（T3.4 spec §5.7）jsonschema 三方断言 ==")
    for name, schema_name in CONTROL_FIXTURES_POSITIVE:
        data = json.loads((repo / "protocol" / "fixtures" / name).read_text(encoding="utf-8"))
        err = validate_schema(data, repo / "protocol" / "schema" / schema_name)
        ck.check(f"ctl.fixture.positive.{name}", err is None, err or "")
    for name, schema_name in CONTROL_FIXTURES_NEGATIVE:
        data = json.loads((repo / "protocol" / "fixtures" / "negative" / name).read_text(encoding="utf-8"))
        err = validate_schema(data, repo / "protocol" / "schema" / schema_name)
        ck.check(f"ctl.fixture.negative.{name}", err is not None,
                 "反例被 schema 放行" if err is None else "")
    # go-actual 基准夹具不变式（spec §5.7.2：serverTime==meta.ts；resumeFromSeq==meta.seq）
    ack = json.loads((repo / "protocol" / "fixtures" / "session-hello-ack.go-actual.json")
                     .read_text(encoding="utf-8"))
    ck.check("ctl.fixture.go_actual_invariants",
             ack["serverTime"] == ack["meta"]["ts"] and ack["resumeFromSeq"] == ack["meta"]["seq"],
             f"{ack['serverTime']}/{ack['meta']['ts']} {ack['resumeFromSeq']}/{ack['meta']['seq']}")


# =====================================================================
# Phase 0：金标准向量复算（独立实现 vs protocol/fixtures/hmac-vector.json）
# =====================================================================
def phase0_golden_vectors(repo: Path, ck: Checks):
    print("\n== Phase 0: 金标准向量复算（fixtures/hmac-vector.json） ==")
    vec_path = repo / "protocol" / "fixtures" / "hmac-vector.json"
    vectors = json.loads(vec_path.read_text(encoding="utf-8"))
    token = vectors["deviceTokenTest"]

    ck.check("vec.empty_payload_sha256",
             vectors["payloadDigestRule"]["emptyPayloadSha256"] == EMPTY_SHA256,
             "空载荷 SHA-256 常量与 spec §5.2.2 冻结值不一致")

    for v in vectors["vectors"]:
        got = hmac_lib.new(bytes.fromhex(token), v["inputString"].encode("utf-8"),
                           hashlib.sha256).hexdigest()
        ck.check(f"vec.hmac.{v['fixture']}", got == v["hmac"],
                 f"复算 {got} != 期望 {v['hmac']}")
        # 输入式逐段复核（C-9：seq 在第 3 段）
        parts = v["inputString"].split("|")
        ck.check(f"vec.input_format.{v['fixture']}",
                 parts == [v.get("commandId", ""), v["eventId"], str(v["seq"]),
                           str(v["ts"]), v["nonce"], v["payloadDigest"]],
                 f"inputString 与输入式不符: {v['inputString']}")

    # 正例夹具 meta.hmac 与向量一致（夹具冻结契约）
    for v in vectors["vectors"]:
        fix = repo / "protocol" / "fixtures" / v["fixture"]
        if not fix.exists():
            continue
        data = json.loads(fix.read_text(encoding="utf-8"))
        meta = data.get("meta", {})
        if "hmac" in meta:
            ck.check(f"vec.fixture_hmac.{v['fixture']}", meta["hmac"] == v["hmac"],
                     f"夹具 meta.hmac 与向量漂移")

    # JCS 互操作：明文夹具 payloadDigest 复算（python JCS vs Go wire/jcs.go）
    plain = json.loads((repo / "protocol" / "fixtures" / "command-envelope.plain.json")
                       .read_text(encoding="utf-8"))
    got = payload_digest(0, plain["list"], "")
    want = vectors["vectors"][0]["payloadDigest"]
    ck.check("vec.jcs_payload_digest.plain", got == want,
             f"python JCS 摘要 {got} != 金标准 {want}（JCS 实现漂移）")

    # 篡改 seq 必败反例（AUD-03 C-9）
    neg = vectors["negative"][0]
    ck.check("vec.negative.case", neg["case"] == "tamper_seq")
    recomputed = hmac_lib.new(bytes.fromhex(token), neg["tamperedInputString"].encode("utf-8"),
                              hashlib.sha256).hexdigest()
    ck.check("vec.negative.hmac_over_tampered", recomputed == neg["hmacOverTamperedInput"],
             f"篡改输入复算 {recomputed} != {neg['hmacOverTamperedInput']}")
    ck.check("vec.negative.must_differ", neg["hmacOverTamperedInput"] != neg["declaredHmac"],
             "篡改 seq 后 HMAC 未变化（C-9 失效）")
    # 篡改输入串 == 原输入串仅 seq 段替换
    orig_parts = vectors["vectors"][0]["inputString"].split("|")
    tamp_parts = neg["tamperedInputString"].split("|")
    tamp_parts[2] = orig_parts[2]
    ck.check("vec.negative.only_seq_changed", tamp_parts == orig_parts,
             "反例输入串与正例差异不止 seq 段")


# =====================================================================
# Phase 2/3 主链路
# =====================================================================
def openapi_post(ck_none, host, port, robot, token, body, idem_key=None, dry_run=False,
                 approval_id=None):
    headers = {"Authorization": f"Bearer {token}"}
    if idem_key is not None:
        headers["Idempotency-Key"] = idem_key
    if dry_run:
        headers["X-WT-Dry-Run"] = "true"
    if approval_id:
        headers["X-WT-Approval-Id"] = approval_id
    return http_json(host, port, "POST", f"/openapi/v1/robots/{robot}/commands",
                     body, headers)


def openapi_get(host, port, robot, token, command_id):
    return http_json(host, port, "GET",
                     f"/openapi/v1/robots/{robot}/commands/{command_id}",
                     headers={"Authorization": f"Bearer {token}"})


def run_e2e(repo: Path, keep: bool, verbose: bool) -> int:
    ck = Checks()
    tmp = Path(tempfile.mkdtemp(prefix="wt-cross-e2e-"))
    proc = None
    con = None
    try:
        phase0_golden_vectors(repo, ck)
        phase0_control_frame_fixtures(repo, ck)

        # ---------------- Phase 1: 构建 + 引导 + 拉起 ----------------
        print("== Phase 1: go build + bootstrap + 启动 ==")
        bin_path = tmp / "wt-server.exe" if os.name == "nt" else tmp / "wt-server"
        build_cmd = ["go", "build", "-C", str(repo / "server"), "-o", str(bin_path),
                     "./cmd/server"]
        print(f"[build] {' '.join(build_cmd)}")
        r = subprocess.run(build_cmd, capture_output=True, text=True,
                           cwd=str(repo / "server"), timeout=300)
        if r.returncode != 0:
            print(f"[env] go build 失败:\n{r.stdout}\n{r.stderr}")
            return 2
        ck.check("build.binary", bin_path.exists())

        port = free_port()
        db_path = tmp / "worktool.db"
        log_out = open(tmp / "server.stdout.log", "wb")
        log_err = open(tmp / "server.stderr.log", "wb")
        proc = subprocess.Popen(
            [str(bin_path), "-listen", f"127.0.0.1:{port}", "-db", str(db_path)],
            stdout=log_out, stderr=log_err)
        if not wait_healthz("127.0.0.1", port, 30):
            print(f"[env] 服务未就绪，stderr:\n{(tmp / 'server.stderr.log').read_text(errors='replace')}")
            return 2
        ck.check("server.healthz", True, f"127.0.0.1:{port}")
        print(f"[up] server pid={proc.pid} listen=127.0.0.1:{port} db={db_path}")

        # bootstrap 种子（与 Go 侧 CreatePairingCode/IssueOpenAPIToken 等价写入）
        pair_code = "".join(secrets.choice("0123456789") for _ in range(6))
        seed_pairing_code(str(db_path), pair_code)
        robot_main = "e2e-robot-01"
        robot_rl = "e2e-robot-rl"
        token_main = secrets.token_hex(32)
        token_rl = secrets.token_hex(32)
        seed_api_token(str(db_path), token_main, robot_main)
        seed_api_token(str(db_path), token_rl, robot_rl)
        ck.check("bootstrap.seeded", True, "pairing_code + 2 api_tokens")

        # ---------------- Phase 2①: python 规范客户端 ----------------
        print("\n== Phase 2①: 规范客户端 配对→hello→SEND_TEXT→结果→幂等→篡改→TTL ==")
        dev = SpecClient("127.0.0.1", port, str(db_path), ck, repo)

        # 2.1 配对负例：无效配对码 → 403 PAIRING_CODE_INVALID（spec §5.1.2）
        st, body = dev.post_pair("000000")
        ck.check("pair.invalid_code_403",
                 st == 403 and body.get("error") == "PAIRING_CODE_INVALID",
                 f"status={st} body={body}")

        # 2.2 配对（Ed25519/X25519 分列两字段，spec §5.1.2）
        st, pr = dev.post_pair(pair_code)
        ck.check("pair.status_201", st == 201, f"status={st} body={pr}")
        if st != 201:
            print(f"[fatal] 配对失败: {pr}")
            return 1
        ck.check("pair.token_256bit", isinstance(pr.get("deviceToken"), str)
                 and len(pr["deviceToken"]) == 64, f"deviceToken={pr.get('deviceToken')}")
        ck.check("pair.frozen_fields",
                 pr.get("tokenVersion") == 1 and pr.get("heartbeatIntervalSeconds") == HEARTBEAT_SECS
                 and pr.get("sessionTimeoutSeconds") == SESSION_TIMEOUT_SECS
                 and pr.get("rotationIntervalDays") == ROTATION_DAYS
                 and pr.get("wsPath", "").startswith("/api/v2/device-sessions/"),
                 f"冻结值漂移: {pr}")
        ck.check("pair.server_x25519", isinstance(pr.get("serverX25519PublicKey"), str)
                 and len(pr["serverX25519PublicKey"]) == 64)
        dev.token_hex = pr["deviceToken"]

        # 2.3 配对码单次有效：同码重放 → 403（spec §5.1.1）
        st, body = dev.post_pair(pair_code)
        ck.check("pair.code_single_use_403",
                 st == 403 and body.get("error") == "PAIRING_CODE_INVALID",
                 f"status={st} body={body}")

        # robot 绑定（控制台职责，admin 占位期 store 层引导；resolveTarget 依赖）
        bind_robot_to_device(str(db_path), dev.device_id, robot_main)

        # 2.4 建会话 → WS → hello → hello_ack
        st, sr = dev.post_device_session()
        ck.check("session.create_201", st == 201, f"status={st} body={sr}")
        if st != 201:
            return 1
        session_id = sr["sessionId"]
        session_key = dev.session_key(sr["serverX25519PublicKey"], session_id)
        dev.dial(sr["wsPath"])
        dev.send_hello(last_epoch=0, last_received_seq=0)
        ack_raw, ack = dev.recv_raw_json(timeout=10)  # ack_raw = 实收原始字节（非再序列化）
        ck.check("hello.ack_type", ack.get("type") == "hello_ack", f"ack={ack}")
        ck.check("hello.ack_frozen",
                 ack.get("heartbeatIntervalSeconds") == HEARTBEAT_SECS
                 and ack.get("sessionTimeoutSeconds") == SESSION_TIMEOUT_SECS,
                 f"{ack}")
        ck.check("hello.resume_from_seq", ack.get("resumeFromSeq") == 1,
                 f"resumeFromSeq={ack.get('resumeFromSeq')}（lastReceivedSeq=0 → 1）")
        # T3.4（spec §5.7）：Go 活体 hello_ack 字节直接过冻结 schema（三方对齐 python 分量）
        ck.check("hello.ack_schema_conform_live",
                 validate_schema(ack, repo / "protocol" / "schema" / "session-hello-ack.schema.json") is None,
                 f"活体 hello_ack 违反冻结 schema: {ack}")
        ck.check("hello.ack_invariants_live",
                 ack.get("serverTime") == ack["meta"]["ts"]
                 and ack.get("resumeFromSeq") == ack["meta"]["seq"],
                 f"{ack}")
        # T3.4 字节级比对：活体 hello_ack vs 冻结基准 fixtures/session-hello-ack.go-actual.json
        # （Go mustJSON=json.Marshal → 紧凑、结构体字段序确定性；基准夹具即此形态采集）
        baseline = json.loads((repo / "protocol" / "fixtures"
                               / "session-hello-ack.go-actual.json").read_text(encoding="utf-8"))
        ck.check("hello.ack_raw_compact_bytes",
                 ack_raw == json.dumps(ack, separators=(",", ":"), ensure_ascii=False),
                 f"活体字节非紧凑确定性形态（含空白/重排）: {ack_raw[:96]!r}")
        ck.check("hello.ack_field_order_baseline",
                 list(ack.keys()) == list(baseline.keys())
                 and list(ack["meta"].keys()) == list(baseline["meta"].keys()),
                 f"live={list(ack.keys())}|{list(ack['meta'].keys())} "
                 f"baseline={list(baseline.keys())}|{list(baseline['meta'].keys())}")
        # 掩蔽会话动态字段（serverTime/meta.ts/nonce/hmac/eventId/seq/resumeFromSeq/
        # tokenVersion）后 JCS 规范形逐字节相等 → 字段全集与冻结常量与基准零漂移
        _mask_paths = [("serverTime",), ("resumeFromSeq",), ("meta", "ts"),
                       ("meta", "nonce"), ("meta", "hmac"), ("meta", "eventId"),
                       ("meta", "seq"), ("meta", "tokenVersion")]

        def _masked(obj):
            out = json.loads(json.dumps(obj))  # deep copy（不动原对象）
            for path in _mask_paths:
                cur = out
                for k in path[:-1]:
                    cur = cur[k]
                cur[path[-1]] = "<DYNAMIC>"
            return out

        ck.check("hello.ack_masked_canonical_baseline",
                 canonical_json(_masked(ack)) == canonical_json(_masked(baseline)),
                 "掩蔽动态字段后 JCS 规范形与冻结基准不一致（字段全集/冻结常量漂移）")
        dev.verify_server_frame_meta("hello_ack", ack["meta"], EMPTY_SHA256)
        dev.last_server_seq = ack["meta"]["seq"]

        # 2.5 SEND_TEXT(203) 干跑全链（FR-54）：服务端代行 + 绝不下发设备
        target_room = "外部群E2E测试群"
        payload203 = {"type": 203, "roomType": 1, "targetRoom": target_room,
                      "receivedContent": "cross-e2e dry-run 消息", "at": [], "atList": []}
        idem_dry = new_uuid()
        st, resp = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                payload203, idem_key=idem_dry, dry_run=True)
        ck.check("dryrun203.http200_code0", st == 200 and resp.get("code") == 0,
                 f"status={st} resp={resp}")
        data = (resp or {}).get("data") or {}
        cmd_dry = data.get("commandId", "")
        ck.check("dryrun203.status_succeeded", data.get("status") == "succeeded"
                 and data.get("dryRun") is True, f"data={data}")
        report = data.get("dryRunReport") or {}
        ck.check("dryrun203.report_fields",
                 report.get("dryRun") is True and report.get("commandId") == cmd_dry
                 and report.get("target") == dev.device_id
                 and report.get("commandType") == 203
                 and report.get("riskClass") == "L0"
                 and report.get("plannedAction") == "发送文本消息"
                 and (report.get("params") or {}).get("receivedContent")
                 == payload203["receivedContent"],
                 f"report={report}")
        ck.check("dryrun203.not_dispatched", dev.expect_silence(2.5),
                 "干跑指令被下发到设备（违反 FR-54 绝不下发）")

        # 2.6 RAW_CONFIRM 收结果（干跑）：终态 + errorCode=0
        st, q = openapi_get("127.0.0.1", port, robot_main, token_main, cmd_dry)
        ck.check("dryrun203.raw_confirm",
                 st == 200 and q.get("code") == 0
                 and (q.get("data") or {}).get("terminal") is True
                 and (q.get("data") or {}).get("finalState") == "succeeded"
                 and (q.get("data") or {}).get("errorCode") == 0,
                 f"status={st} q={q}")

        # 2.7 幂等键（spec §5.6.1）：同键同参 → 原响应重放；同键变参 → 409
        st, resp2 = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                 payload203, idem_key=idem_dry, dry_run=True)
        d2 = (resp2 or {}).get("data") or {}
        ck.check("idem.same_key_same_params",
                 st == 200 and d2.get("replay") is True and d2.get("commandId") == cmd_dry,
                 f"status={st} data={d2}")
        payload203_b = dict(payload203, receivedContent="变参载荷")
        st, resp3 = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                 payload203_b, idem_key=idem_dry, dry_run=True)
        ck.check("idem.same_key_diff_params_409",
                 st == 409 and resp3.get("code") == 101007,
                 f"status={st} resp={resp3}")

        # 2.8 真实下发链：提交（非干跑）→ 调度泵下发 → GCM 解密互操作（§5.3）
        idem_real = new_uuid()
        st, resp = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                payload203, idem_key=idem_real)
        cmd_real = ((resp or {}).get("data") or {}).get("commandId", "")
        ck.check("real203.accepted", st == 200 and cmd_real != "", f"status={st} resp={resp}")
        frame = dev.recv_command_frame(session_key, cmd_real, timeout=15)
        ck.check("real203.frame_received", frame is not None, "15s 内未收到下发指令帧")
        if frame is None:
            return 1
        meta = frame["meta"]
        digest2 = payload_digest(2, None, frame.get("encryptedList", ""))
        dev.verify_server_frame_meta("real203.frame", meta, digest2)
        ck.check("real203.frame_encrypt_type2", frame.get("encryptType") == 2,
                 f"encryptType={frame.get('encryptType')}（capabilities.encryptTypeMax=2）")
        ck.check("real203.frame_socket_type", frame.get("socketType") == 2)
        plain_list = dev.decrypt_frame_list(frame, session_key)  # GCM 失败即抛异常
        ck.check("real203.gcm_decrypt_payload",
                 isinstance(plain_list, list) and plain_list[0].get("type") == 203
                 and plain_list[0].get("targetRoom") == target_room,
                 f"plain={plain_list}")
        # T3.4（spec §5.7.2 v1.2.0）：dispatch 载荷携带 epoch（端侧拒旧水位的线缆输入）
        ck.check("real203.dispatch_epoch_present",
                 isinstance(plain_list[0].get("epoch"), int) and plain_list[0]["epoch"] >= 1,
                 f"epoch={plain_list[0].get('epoch')}")
        ck.check("real203.frame_seq_increment", meta["seq"] == dev.last_server_seq,
                 f"seq={meta['seq']}")

        # 2.9 设备结果回执（socketType=3）→ E05→E09→E10 succeeded + 墓碑
        result_event = new_uuid()
        dev.send_exec_result(cmd_real, result_event, 203, 0, target_room, seq=2)
        terminal = {}
        for _ in range(20):
            st, q = openapi_get("127.0.0.1", port, robot_main, token_main, cmd_real)
            terminal = (q or {}).get("data") or {}
            if terminal.get("terminal"):
                break
            time.sleep(0.5)
        ck.check("real203.result_terminal_succeeded",
                 terminal.get("finalState") == "succeeded" and terminal.get("errorCode") == 0,
                 f"q={terminal}")
        tb = db_query_one(str(db_path),
                          "SELECT final_state FROM tombstones WHERE command_id=?", (cmd_real,))
        ck.check("real203.tombstone_created",
                 tb is not None and tb[0] == "succeeded", f"tombstones={tb}")
        tb_full = db_query_one(str(db_path),
                               "SELECT terminal_at, purge_after FROM tombstones WHERE command_id=?",
                               (cmd_real,))
        if tb_full:
            retention_days = (tb_full[1] - tb_full[0]) / 86400000
            ck.check("real203.tombstone_retention_ge7d", retention_days >= TOMBSTONE_MIN_DAYS,
                     f"保留 {retention_days:.2f} 天 < {TOMBSTONE_MIN_DAYS}")
        else:
            ck.check("real203.tombstone_retention_ge7d", False, "墓碑行缺失")

        # 2.10 墓碑重放同 eventId 幂等（§5.4.3 同 ID 同内容 → 幂等，不重复副作用）
        outbox_before = db_query_one(
            str(db_path),
            "SELECT COUNT(*) FROM outbox WHERE command_id=? AND kind='exec_result'",
            (cmd_real,))[0]
        dev.send_exec_result(cmd_real, result_event, 203, 0, target_room, seq=3,
                             label="execresult_replay")
        time.sleep(1.5)
        outbox_after = db_query_one(
            str(db_path),
            "SELECT COUNT(*) FROM outbox WHERE command_id=? AND kind='exec_result'",
            (cmd_real,))[0]
        e10_count = db_query_one(
            str(db_path),
            "SELECT COUNT(*) FROM command_events WHERE command_id=? AND edge_id='E10'",
            (cmd_real,))[0]
        ck.check("replay.no_duplicate_side_effect",
                 outbox_after == outbox_before == 1 and e10_count == 1,
                 f"outbox {outbox_before}->{outbox_after}, E10×{e10_count}")
        ck.check("replay.session_alive", dev.expect_silence(1.5),
                 "重放帧触发连接关闭（应为幂等静默）")

        # 2.11 篡改 seq 必拒（C-9：seq 纳入 HMAC → 4401）
        heartbeat = {"socketType": 0, "meta": None, "encryptType": 0, "list": []}
        declared_seq, hmac_seq = 4, 5
        heartbeat["meta"] = build_meta(dev.token_hex, "", declared_seq, EMPTY_SHA256,
                                       dev.token_version, hmac_seq=hmac_seq)
        dev.send_json(heartbeat)
        close_code, close_reason = dev.server_close()
        ck.check("tamper_seq.rejected_4401", close_code == CLOSE_AUTH_FAILURE,
                 f"close_code={close_code} reason={close_reason}（期望 4401 HMAC 验签失败）")
        ck.check("tamper_seq.close_reason_hmac", "HMAC" in close_reason,
                 f"reason={close_reason}")
        try:
            dev.ws.close()
        except Exception:
            pass

        # 2.12 TTL 到期对账路径（E17，时钟注入 = ack_deadline 回拨，见模块 docstring）
        idem_ttl = new_uuid()
        st, resp = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                payload203, idem_key=idem_ttl)
        cmd_ttl = ((resp or {}).get("data") or {}).get("commandId", "")
        ck.check("ttl.submit_queued", st == 200 and cmd_ttl != "", f"status={st} resp={resp}")
        # 重连（4401 关闭后的会话重建；凭据仍在内存）
        st, sr2 = dev.post_device_session()
        ck.check("ttl.reconnect_session", st == 201, f"status={st} body={sr2}")
        if st != 201:
            return 1
        session_id2 = sr2["sessionId"]
        session_key2 = dev.session_key(sr2["serverX25519PublicKey"], session_id2)
        dev.dial(sr2["wsPath"])
        dev.send_hello(last_epoch=0, last_received_seq=dev.last_server_seq,
                       label="hello_reconn_ttl")
        ack2 = dev.recv_json(timeout=10)
        ck.check("ttl.hello_ack_resume",
                 ack2.get("type") == "hello_ack"
                 and ack2.get("resumeFromSeq") == dev.last_server_seq + 1,
                 f"ack={ack2}")
        frame2 = dev.recv_command_frame(session_key2, cmd_ttl, timeout=15)
        ck.check("ttl.frame_dispatched", frame2 is not None, "重连后未补发 queued 指令")
        if frame2 is None:
            return 1
        st, q = openapi_get("127.0.0.1", port, robot_main, token_main, cmd_ttl)
        state = ((q or {}).get("data") or {}).get("status")
        ck.check("ttl.state_dispatched", state == "dispatched", f"status={state}")
        row = db_query_one(str(db_path),
                           "SELECT dispatched_at, ack_deadline FROM commands WHERE command_id=?",
                           (cmd_ttl,))
        if row and row[0] and row[1]:
            ttl_span = (row[1] - row[0]) / 1000
            ck.check("ttl.ack_deadline_300s", 290 <= ttl_span <= 310,
                     f"TTL 跨度 {ttl_span:.1f}s（期望 {ACK_TTL_SECONDS}s）")
        else:
            ck.check("ttl.ack_deadline_300s", False, f"ack_deadline 缺失: {row}")
        # 时钟注入：deadline 回拨到过去 → RunAckTTLScanner(10s) → E17
        db_exec(str(db_path), "UPDATE commands SET ack_deadline=? WHERE command_id=?",
                (now_ms() - 60000, cmd_ttl))
        migrated = False
        final_state = None
        for _ in range(15):
            st, q = openapi_get("127.0.0.1", port, robot_main, token_main, cmd_ttl)
            final_state = ((q or {}).get("data") or {}).get("status")
            if final_state == "execution_unknown":
                migrated = True
                break
            time.sleep(2)
        ck.check("ttl.e17_execution_unknown", migrated,
                 f"最终状态={final_state}（期望 execution_unknown）")
        if migrated:
            qd = ((openapi_get("127.0.0.1", port, robot_main, token_main, cmd_ttl)[1] or {})
                  .get("data") or {})
            ck.check("ttl.no_outcome_fabricated",
                     qd.get("terminal") is False and qd.get("status") not in ("succeeded", "failed"),
                     f"q={qd}（禁止超时定性成败，三原则①）")
            e17 = db_query_one(str(db_path),
                               "SELECT COUNT(*) FROM command_events WHERE command_id=? AND edge_id='E17'",
                               (cmd_ttl,))[0]
            ck.check("ttl.e17_edge_logged", e17 == 1, f"E17 留痕 ×{e17}")
            tb = db_query_one(str(db_path),
                              "SELECT COUNT(*) FROM tombstones WHERE command_id=?", (cmd_ttl,))[0]
            ck.check("ttl.no_tombstone_before_terminal", tb == 0,
                     f"非终态出现墓碑 ×{tb}")

        # ---------------- 2.13: 903 RESYNC 真往返（T3.4 控制帧冻结） ----------------
        # 真实签名乱序 seq（跳号）→ 服务端先发 [{type:903}] 载荷帧、随后 4405 关闭
        # （spec §5.4.2/§5.4.4；Go sendResync 现产形态 = 冻结基准）。
        print("\n== Phase 2.13: 903 RESYNC_REQUEST 真往返 ==")
        skip_seq = dev.in_seq + 2  # 期望 in_seq+1，实际发 in_seq+2（跳号）
        skip_meta = build_meta(dev.token_hex, "", skip_seq, EMPTY_SHA256, dev.token_version)
        skip_frame = {"socketType": 0, "messageId": build_message_id(now_ms()),
                      "meta": skip_meta, "apiSend": 0, "encryptType": 0, "list": []}
        frames_before_close = []
        close_after_903 = None
        dev.send_json(skip_frame)
        deadline = time.time() + 8
        while time.time() < deadline:
            try:
                opcode, data = dev.ws.recv_data(control_frame=False)
            except Exception as e:
                close_after_903 = getattr(dev.ws, "close_status", None) or f"recv: {e}"
                break
            if opcode == websocket.ABNF.OPCODE_CLOSE:
                close_after_903 = int.from_bytes(data[:2], "big") if len(data) >= 2 else None
                break
            if opcode == websocket.ABNF.OPCODE_TEXT:
                frames_before_close.append(json.loads(data.decode("utf-8", "replace")))
        ctl_903 = None
        ctl_903_item = None  # 活体 903 载荷元素（schema 校验输入 = 实收字节，非字面量）
        for f in frames_before_close:
            if "meta" not in f:
                continue
            items = f.get("list") or []
            if f.get("encryptType") == 2:
                items = dev.decrypt_frame_list(f, session_key2)
            hit = next((i for i in items if isinstance(i, dict) and i.get("type") == 903), None)
            if hit is not None:
                ctl_903 = f
                ctl_903_item = hit
                dev.last_server_seq = max(dev.last_server_seq, f["meta"]["seq"])
        ck.check("ctl903.frame_received_before_close", ctl_903 is not None,
                 f"关闭前帧={[list(f.keys()) for f in frames_before_close]}")
        if ctl_903 is not None:
            ck.check("ctl903.schema_conform",
                     validate_schema(ctl_903_item,
                                     repo / "protocol" / "schema" / "control-payload.schema.json") is None,
                     "903 活体载荷违反冻结 schema")
            ck.check("ctl903.frozen_form_deep_equal",
                     ctl_903_item == {"type": 903},
                     f"Go 现产 903 载荷漂移于冻结基准 {{type:903}}: {ctl_903_item!r}")
            digest903 = payload_digest(2, None, ctl_903.get("encryptedList", "")) \
                if ctl_903.get("encryptType") == 2 else EMPTY_SHA256
            dev.verify_server_frame_meta("ctl903", ctl_903["meta"], digest903)
        ck.check("ctl903.close_4405_after_resync", close_after_903 == 4405,
                 f"close={close_after_903}（期望 4405）")
        try:
            dev.ws.close()
        except Exception:
            pass

        # ---------------- 2.14: 904 JOURNAL_REPLAY 真往返（T3.4） ----------------
        # 存在 pending execution_unknown 时重连 → 服务端 hello_ack 后下发
        # [{type:904,request:"journal_replay"}]（Go sendJournalRequest 现产形态）；
        # python 以 cmd_ttl 的持久证据补报（outcome=success）→ E12 对账离出 → succeeded + 墓碑。
        print("\n== Phase 2.14: 904 JOURNAL_REPLAY 真往返 ==")
        st, sr3 = dev.post_device_session()
        ck.check("ctl904.reconnect_session", st == 201, f"status={st} body={sr3}")
        if st != 201:
            return 1
        session_id3 = sr3["sessionId"]
        session_key3 = dev.session_key(sr3["serverX25519PublicKey"], session_id3)
        dev.dial(sr3["wsPath"])
        dev.send_hello(last_epoch=0, last_received_seq=dev.last_server_seq,
                       label="hello_reconn_904")
        ack3 = dev.recv_json(timeout=10)
        ck.check("ctl904.hello_ack_resume_continuous",
                 ack3.get("type") == "hello_ack"
                 and ack3.get("resumeFromSeq") == dev.last_server_seq + 1,
                 f"ack={ack3}（服务端出向 seq 跨连接连续，spec §5.4.2 v1.2.0）")
        dev.last_server_seq = ack3["meta"]["seq"]

        # 读至 904-request 帧（hello_ack 后紧随；无 queued 指令时无其他业务帧）
        ctl_904req = None
        ctl_904req_item = None  # 活体 904-request 载荷元素（schema 校验输入 = 实收字节，非字面量）
        deadline = time.time() + 10
        while time.time() < deadline:
            try:
                frame = dev.recv_json(timeout=max(1, deadline - time.time()))
            except websocket.WebSocketTimeoutException:
                break
            except Exception:
                break
            if not isinstance(frame, dict) or "meta" not in frame:
                continue
            items = frame.get("list") or []
            if frame.get("encryptType") == 2:
                items = dev.decrypt_frame_list(frame, session_key3)
            dev.last_server_seq = max(dev.last_server_seq, frame["meta"]["seq"])
            hit = next((i for i in items if isinstance(i, dict) and i.get("type") == 904
                        and i.get("request") == "journal_replay"), None)
            if hit is not None:
                ctl_904req = frame
                ctl_904req_item = hit
                break
        ck.check("ctl904.request_received", ctl_904req is not None,
                 "重连后未收到 904 journal_replay 请求（PendingUnknownRisk 应非空）")
        if ctl_904req is not None:
            ck.check("ctl904.request_schema_conform",
                     validate_schema(ctl_904req_item,
                                     repo / "protocol" / "schema" / "control-payload.schema.json") is None,
                     "904-request 活体载荷违反冻结 schema")
            ck.check("ctl904.request_frozen_form_deep_equal",
                     ctl_904req_item == {"type": 904, "request": "journal_replay"},
                     f"Go 现产 904-request 载荷漂移于冻结基准: {ctl_904req_item!r}")

        # 端侧补报：cmd_ttl 持久证据（epoch 取服务端 commands.epoch，outcome=success）
        row = db_query_one(str(db_path), "SELECT epoch FROM commands WHERE command_id=?", (cmd_ttl,))
        cmd_ttl_epoch = row[0] if row and row[0] else 0
        replay_entry = {"commandId": cmd_ttl, "epoch": cmd_ttl_epoch,
                        "started": True, "outcome": "success", "source": "local"}
        ck.check("ctl904.replay_entry_schema_conform",
                 validate_schema({"type": 904, "entries": [replay_entry]},
                                 repo / "protocol" / "schema" / "control-payload.schema.json") is None,
                 "904 补报载荷违反冻结 schema")
        replay_item = {"type": 904, "entries": [replay_entry]}
        digest904 = payload_digest(0, [replay_item], "")
        dev.send_json({"socketType": 2, "messageId": build_message_id(now_ms()),
                       "meta": dev.next_meta("", digest904),
                       "apiSend": 0, "encryptType": 0, "list": [replay_item]})

        reconciled = {}
        for _ in range(20):
            st, q = openapi_get("127.0.0.1", port, robot_main, token_main, cmd_ttl)
            reconciled = (q or {}).get("data") or {}
            if reconciled.get("terminal"):
                break
            time.sleep(0.5)
        ck.check("ctl904.e12_reconciled_succeeded",
                 reconciled.get("finalState") == "succeeded" and reconciled.get("errorCode") == 0,
                 f"q={reconciled}（execution_unknown → E12 证据·成功）")
        e12 = db_query_one(str(db_path),
                           "SELECT COUNT(*) FROM command_events WHERE command_id=? AND edge_id='E12'",
                           (cmd_ttl,))[0]
        ck.check("ctl904.e12_edge_logged", e12 == 1, f"E12 留痕 ×{e12}")
        tb904 = db_query_one(str(db_path),
                             "SELECT COUNT(*) FROM tombstones WHERE command_id=?", (cmd_ttl,))[0]
        ck.check("ctl904.tombstone_after_reconcile", tb904 == 1, f"墓碑 ×{tb904}")
        # 904 幂等：同 commandId 重发补报 → 不重复副作用（E12 仍 1 条）
        digest904b = payload_digest(0, [replay_item], "")
        replay2 = {"type": 904, "entries": [replay_entry]}
        meta904b = build_meta(dev.token_hex, "", dev.in_seq + 1, digest904b, dev.token_version)
        dev.send_json({"socketType": 2, "messageId": build_message_id(now_ms()),
                       "meta": meta904b, "apiSend": 0, "encryptType": 0, "list": [replay2]})
        dev.in_seq += 1
        time.sleep(1.5)
        e12b = db_query_one(str(db_path),
                            "SELECT COUNT(*) FROM command_events WHERE command_id=? AND edge_id='E12'",
                            (cmd_ttl,))[0]
        ck.check("ctl904.replay_idempotent", e12b == 1, f"E12 ×{e12b}（重放不得重复迁移）")

        # ---------------- Phase 3②: apifox 兼容面冒烟 ----------------
        print("\n== Phase 3②: /openapi/v1 apifox 冒烟 ==")
        # 3.1 缺幂等键 → 400 101003（§5.6.1 全 POST 强制）
        st, resp = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                payload203, idem_key=None, dry_run=True)
        ck.check("openapi.idem_key_required_400",
                 st == 400 and resp.get("code") == 101003, f"status={st} resp={resp}")

        # 3.2 206 INIT_GROUP 干跑全链（L1：白名单未配置不拦截 + 限速）
        payload206 = {"type": 206, "targetRoom": "E2E-建群-干跑",
                      "groupName": "E2E-建群-干跑", "groupNotice": "", "memberList": []}
        st, resp = openapi_post(ck, "127.0.0.1", port, robot_main, token_main,
                                payload206, idem_key=new_uuid(), dry_run=True)
        d206 = ((resp or {}).get("data") or {})
        r206 = d206.get("dryRunReport") or {}
        ck.check("dryrun206.full_chain",
                 st == 200 and resp.get("code") == 0 and d206.get("status") == "succeeded"
                 and r206.get("riskClass") == "L1"
                 and r206.get("plannedAction") == "创建群（拉人/公告/备注/模板）",
                 f"status={st} data={d206}")

        # 3.3 RAW_CONFIRM 404 负例
        st, resp = openapi_get("127.0.0.1", port, robot_main, token_main, new_uuid())
        ck.check("openapi.raw_confirm_404",
                 st == 404 and resp.get("code") == 101010, f"status={st} resp={resp}")

        # 3.4 60QPM 限流负例（独立 robot 桶：60 次 dry-run 干跑通过 → 第 61 次 429）
        allowed, limited = 0, None
        for i in range(61):
            st, resp = openapi_post(ck, "127.0.0.1", port, robot_rl, token_rl,
                                    payload203, idem_key=new_uuid(), dry_run=True)
            if st == 429:
                limited = (st, resp)
                break
            allowed += 1
        ck.check("ratelimit.60qpm_allows_60", allowed >= 60,
                 f"第 {allowed+1} 次即被限流（应放行 60）")
        ck.check("ratelimit.429_code_101004",
                 limited is not None and limited[0] == 429
                 and limited[1].get("code") == 101004,
                 f"limited={limited}")

        # ---------------- Phase 4③: 服务清理 ----------------
        print("\n== Phase 4③: 服务清理 ==")
        try:
            dev.ws.close()
        except Exception:
            pass
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=10)
        ck.check("cleanup.process_exited", proc.returncode is not None,
                 f"returncode={proc.returncode}")
        proc = None
        log_out.close()
        log_err.close()

        ck.print_summary()
        return 0 if ck.summary_line().endswith("result=PASS") else 1
    finally:
        if proc is not None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
        if con is not None:
            con.close()
        if not keep and tmp.exists():
            shutil.rmtree(tmp, ignore_errors=True)
        else:
            print(f"[keep] 工作目录保留: {tmp}")


def main():
    ap = argparse.ArgumentParser(description="WorkTool-Open 跨实现一致性 E2E")
    ap.add_argument("--repo-root", default=None, help="仓库根（默认 = 脚本上两级）")
    ap.add_argument("--keep", action="store_true", help="保留临时工作目录")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args()

    repo = Path(args.repo_root).resolve() if args.repo_root \
        else Path(__file__).resolve().parents[2]
    for probe in ["protocol/fixtures/hmac-vector.json", "server/cmd/server/main.go"]:
        if not (repo / probe).exists():
            print(f"[env] 仓库根不正确，缺 {probe}: {repo}", file=sys.stderr)
            sys.exit(2)

    try:
        rc = run_e2e(repo, args.keep, args.verbose)
    except KeyboardInterrupt:
        print("[abort] 手动中断", file=sys.stderr)
        rc = 2
    sys.exit(rc)


if __name__ == "__main__":
    main()
