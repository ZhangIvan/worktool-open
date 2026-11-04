/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package gateway_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cn.worktool.open/server/internal/admin"
	"cn.worktool.open/server/internal/wire"
)

// ed25519Generate 测试内独立生成（与 pairDevice 同源语义）。
func ed25519Generate() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return toHex(pub), toHex(priv), nil
}

// issueAdminTokenFor 经 api 侧 Server 无法直达（admin 独立装配）——本测试 app 未
// 挂 admin 面，直接以 SQL 落 admin scope 令牌哈希（与 IssueAdminToken 同型）。
func issueAdminTokenFor(t *testing.T, a *testApp) string {
	t.Helper()
	raw, err := wire.RandomHex(32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(context.Background(),
		`INSERT INTO api_tokens(token_hash, robot_id, tenant, status, created_ms, last_used_ms)
		 VALUES (?, 'admin', 'default', 'active', 0, 0)`, sha256Hex(raw)); err != nil {
		t.Fatalf("seed admin token: %v", err)
	}
	return raw
}

// req 带 Bearer 的任意方法请求。
func (a *testApp) req(t *testing.T, method, path, body, token string) httpResp {
	t.Helper()
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	rq, err := http.NewRequest(method, a.httpSrv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	rq.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return httpResp{code: resp.StatusCode, body: buf.Bytes()}
}

// TestPairIdentityMergeByEd25519（2026-11-04 所有者反馈①：同一物理设备不得多条
// 记录）：同一 Ed25519 身份公钥、不同 deviceId（重装/换前缀形态）两次配对 →
// devices 表恒一行，且第二次响应返回**既有** deviceId（身份归并）。
func TestPairIdentityMergeByEd25519(t *testing.T) {
	a := newTestApp(t)

	pair := func(deviceID string, edPubHex string) string {
		t.Helper()
		code, err := a.apiSrv.CreatePairingCode()
		if err != nil {
			t.Fatalf("签发配对码: %v", err)
		}
		xPriv, xPub, err := wire.GenerateX25519()
		if err != nil {
			t.Fatal(err)
		}
		resp := a.post(t, "/api/v2/pair", map[string]string{
			"pairingCode":              code,
			"deviceId":                 deviceID,
			"deviceName":               "identity-merge-test",
			"ed25519PublicKey":         edPubHex,
			"x25519EphemeralPublicKey": xPub,
		})
		_ = xPriv
		if resp.code != http.StatusCreated {
			t.Fatalf("pair(%s) = %d: %s", deviceID, resp.code, resp.body)
		}
		var pr struct {
			DeviceID string `json:"deviceId"`
		}
		if err := json.Unmarshal([]byte(resp.body), &pr); err != nil {
			t.Fatalf("响应解析: %v", err)
		}
		return pr.DeviceID
	}

	// 同一身份公钥（模拟设备密钥持久化），两个不同 deviceId 形态
	edPub, _, err := ed25519Generate()
	if err != nil {
		t.Fatal(err)
	}
	first := pair("device-era-a", edPub)
	second := pair("device-era-b", edPub)

	if first != second {
		t.Fatalf("同 Ed25519 身份须归并到既有 deviceId：first=%s second=%s", first, second)
	}
	rows, err := a.db.QueryDump(context.Background(), `SELECT COUNT(*) FROM devices WHERE ed25519_public_key = ?`, edPub)
	if err != nil || len(rows) != 1 {
		t.Fatalf("count: %v", err)
	}
	if rows[0][0] != "1" {
		t.Fatalf("同身份公钥必须单行记录，实际 %s 行", rows[0][0])
	}
}

// TestAdminDeviceDelete：管理面 delete 动作删除陈旧行（配合身份归并清理存量
// 异钥旧记录）；不可复删（第二次 404）。
func TestAdminDeviceDelete(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	_ = m

	// admin 面独立装配（main 同型：New+Register 到同一 mux 之外的轻量 httptest）
	adminSrv := admin.New(admin.Deps{DB: a.db, Pipeline: a.pipeline, Log: testLogger{t}})
	admMux := http.NewServeMux()
	adminSrv.Register(admMux)
	admURL := httptest.NewServer(admMux).URL
	doAdmin := func(method, path, token string) httpResp {
		rq, _ := http.NewRequest(method, admURL+path, bytes.NewReader(nil))
		rq.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return httpResp{code: resp.StatusCode, body: buf.Bytes()}
	}

	token := issueAdminTokenFor(t, a)
	del := doAdmin(http.MethodPost, "/api/v1/admin/devices/"+m.deviceID+"/delete", token)
	if del.code != http.StatusOK {
		t.Fatalf("delete = %d: %s", del.code, del.body)
	}
	rows, _ := a.db.QueryDump(context.Background(), `SELECT COUNT(*) FROM devices WHERE device_id = ?`, m.deviceID)
	if rows[0][0] != "0" {
		t.Fatalf("设备行未删除")
	}
	again := doAdmin(http.MethodPost, "/api/v1/admin/devices/"+m.deviceID+"/delete", token)
	if again.code != http.StatusNotFound {
		t.Fatalf("复删应 404，实际 %d", again.code)
	}
}
