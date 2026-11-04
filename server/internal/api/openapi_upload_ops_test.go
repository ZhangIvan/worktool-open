// W-ops 审计项回归测试：③ clientlog 磁盘配额（200MiB/robot/UTC 当日，超限 429）
// + ④ /files/{fileId} 可选 HMAC 签名取回（exp/sig，密钥复用 CallbackSecret）
// + 无签名参数保持能力 URL 现状但 Cache-Control: no-store。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clientlogUpload 发起一次 clientlog 上传，返回状态码 + 信封。
func clientlogUpload(t *testing.T, f *openapiFixture, robotID string, payload []byte) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("robotId", robotID)
	_ = w.WriteField("clientVersion", "0.1.0-test")
	part, _ := w.CreateFormFile("file", "log.zip")
	part.Write(payload)
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/robot/clientLog/upload?robotId="+robotID, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Idempotency-Key", "clog-quota-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

// TestClientLogUploadDailyQuota（W-ops ③）：当日累计读 clientlogs 现存文件大小和
//（robot 维度经元数据表归属），超 200MiB 配额（测试注入 48B 小配额）→ 429；
// robot 间隔离；文件被清理即释放配额。
func TestClientLogUploadDailyQuota(t *testing.T) {
	f := newOpenAPIFixture(t)
	f.api.FileDir = t.TempDir() // 隔离落盘（不写仓库 wt-files）
	oldQuota := clientlogQuotaPerRobotPerDay
	clientlogQuotaPerRobotPerDay = 48
	defer func() { clientlogQuotaPerRobotPerDay = oldQuota }()

	const blob20 = "01234567890123456789" // 20 字节
	st, env := clientlogUpload(t, f, "robot-quota-a", []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("首次上传应 200，got %d %v", st, env)
	}
	st, env = clientlogUpload(t, f, "robot-quota-a", []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("累计 40B ≤ 48B 应 200，got %d %v", st, env)
	}
	// 第三次：40+20=60 > 48 → 429。
	st, env = clientlogUpload(t, f, "robot-quota-a", []byte(blob20))
	if st != http.StatusTooManyRequests {
		t.Fatalf("超配额应 429，got %d %v", st, env)
	}
	// robot 隔离：robot-b 从零计账，仍可上传。
	st, _ = clientlogUpload(t, f, "robot-quota-b", []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("另一 robot 不受 a 配额影响应 200，got %d", st)
	}
	// 现存文件口径：清理 robot-a 的一个落盘文件 → 占用回落 20B → 再传 20B 可通过。
	logsDir := filepath.Join(f.api.FileDir, "clientlogs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, e := range entries {
		if removed == 1 {
			break
		}
		// 只删 robot-a 的文件（robot-b 仅有 1 个，逐个按元数据归属核对）。
		rows, _ := f.db.QueryDump(context.Background(), `SELECT robot_id FROM clientlog_uploads WHERE file_id = ?`, strings.TrimSuffix(e.Name(), ".zip"))
		if len(rows) == 1 && rows[0][0] == "robot-quota-a" {
			if err := os.Remove(filepath.Join(logsDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	if removed != 1 {
		t.Fatalf("应能按元数据定位并删除 robot-a 的一个文件，removed=%d", removed)
	}
	st, _ = clientlogUpload(t, f, "robot-quota-a", []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("清理现存文件释放配额后应 200（20 现存+20 新=40 ≤ 48），got %d", st)
	}
}

// fileGetStatus GET /files/{id}（带可选 query），返回状态码 + Cache-Control 头。
func fileGetStatus(t *testing.T, f *openapiFixture, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Cache-Control")
}

// uploadOneFile 经 /fileUpload/upload 落一个文件，返回 fileId。
func uploadOneFile(t *testing.T, f *openapiFixture) string {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "hello.txt")
	part.Write([]byte("hello file body"))
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/fileUpload/upload?robotId="+f.robot, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "files-sig-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Data struct {
			FileID string `json:"fileId"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Data.FileID == "" {
		t.Fatalf("上传应返回 fileId")
	}
	return env.Data.FileID
}

// TestFileGetSignedAccess（W-ops ④）：无签名参数保持能力 URL 现状 + no-store；
// 签名参数在场即强制校验（密钥复用 CallbackSecret）：缺一 400 / 过期 403 /
// 不匹配 403 / 正确 200 / 密钥未配置 fail-closed 403。
func TestFileGetSignedAccess(t *testing.T) {
	f := newOpenAPIFixture(t)
	f.api.FileDir = t.TempDir()
	fileID := uploadOneFile(t, f)

	// 无签名参数：现状放行（能力 URL），响应头 no-store（原 private,max-age=3600 废除）。
	st, cc := fileGetStatus(t, f, "/files/"+fileID)
	if st != http.StatusOK {
		t.Fatalf("无签名参数能力 URL 应 200，got %d", st)
	}
	if cc != "no-store" {
		t.Fatalf("Cache-Control 应为 no-store，got %q", cc)
	}

	// 密钥未装配（Outbox nil）：签名参数在场 → fail-closed 403。
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)+"&sig=aa")
	if st != http.StatusForbidden {
		t.Fatalf("密钥未配置签名取回应 403 fail-closed，got %d", st)
	}

	// 装配 worker（CallbackSecret=k1）后逐态校验。
	worker, err := NewOutboxWorker(f.db, OutboxConfig{CallbackSecret: "k1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.api.Outbox = worker
	// exp 相对**服务端注入时钟**（fixture f.now 固定基准）计算——真实墙钟在此无关。
	nowSrv := (*f.now).UTC()
	future := nowSrv.Add(time.Hour).UnixMilli()
	past := nowSrv.Add(-time.Minute).UnixMilli()

	good := signFileAccess("k1", fileID, future)
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(future, 10)+"&sig="+good)
	if st != http.StatusOK {
		t.Fatalf("正确签名应 200，got %d", st)
	}
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(future, 10)+"&sig=deadbeef")
	if st != http.StatusForbidden {
		t.Fatalf("签名不匹配应 403，got %d", st)
	}
	// 换 exp 即换消息：旧 exp 的签名对另一 exp 失效。
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(past+1, 10)+"&sig="+good)
	if st != http.StatusForbidden {
		t.Fatalf("exp 变更后旧签名应失效 403，got %d", st)
	}
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(past, 10)+"&sig="+signFileAccess("k1", fileID, past))
	if st != http.StatusForbidden {
		t.Fatalf("过期 exp 应 403，got %d", st)
	}
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(future, 10))
	if st != http.StatusBadRequest {
		t.Fatalf("只带 exp 缺 sig 应 400，got %d", st)
	}
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp=not-a-number&sig="+good)
	if st != http.StatusBadRequest {
		t.Fatalf("exp 非整数应 400，got %d", st)
	}
	// 签名对其他 fileId 不换算有效（消息绑定 fileId）。
	other := signFileAccess("k1", "other-file-0001", future)
	st, _ = fileGetStatus(t, f, "/files/"+fileID+"?exp="+strconv.FormatInt(future, 10)+"&sig="+other)
	if st != http.StatusForbidden {
		t.Fatalf("他人 fileID 签名应 403，got %d", st)
	}
}
