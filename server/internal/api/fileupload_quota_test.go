// /fileUpload/upload 当日磁盘配额回归测试（复用 clientlog ③配额形态）：
// 单 robot 当日（服务端 UTC）累计 ≤ 200MiB（测试注入 48B 小配额），超限 429 +
// 审计落库；robot 间隔离；文件被清理（现存口径）即释放配额；被拒上传不留落盘
// 孤儿字节。
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

// fileUploadTok 以指定 robot 令牌发起一次 /fileUpload/upload，返回状态码 + 信封
// （token 与 robotId 同源——本面鉴权核对 token 归属，robot 隔离用例须换发令牌）。
func fileUploadTok(t *testing.T, f *openapiFixture, robotID, token string, payload []byte) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "shot.png")
	part.Write(payload)
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/fileUpload/upload?robotId="+robotID, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "file-quota-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

// fileUpload 发起一次 /fileUpload/upload（本 fixture 令牌，归属 f.robot）。
func fileUpload(t *testing.T, f *openapiFixture, robotID string, payload []byte) (int, map[string]any) {
	t.Helper()
	return fileUploadTok(t, f, robotID, f.token, payload)
}

// TestFileUploadDailyQuota：超配额 429 + 审计；隔离；现存文件口径释放配额；
// 拒绝路径不留孤儿文件。
func TestFileUploadDailyQuota(t *testing.T) {
	f := newOpenAPIFixture(t)
	f.api.FileDir = t.TempDir() // 隔离落盘（不写仓库 wt-files）
	oldQuota := fileUploadQuotaPerRobotPerDay
	fileUploadQuotaPerRobotPerDay = 48
	defer func() { fileUploadQuotaPerRobotPerDay = oldQuota }()

	const blob20 = "01234567890123456789" // 20 字节
	st, env := fileUpload(t, f, f.robot, []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("首次上传应 200，got %d %v", st, env)
	}
	st, env = fileUpload(t, f, f.robot, []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("累计 40B ≤ 48B 应 200，got %d %v", st, env)
	}
	// 第三次：40+20=60 > 48 → 429。
	st, env = fileUpload(t, f, f.robot, []byte(blob20))
	if st != http.StatusTooManyRequests {
		t.Fatalf("超配额应 429，got %d %v", st, env)
	}
	if code, _ := env["code"].(float64); int(code) != openapiCodeRateLimited {
		t.Fatalf("429 信封 code 应为 %d（openapiCodeRateLimited），got %v", openapiCodeRateLimited, env["code"])
	}
	if msg, _ := env["message"].(string); !strings.Contains(msg, "当日磁盘配额") {
		t.Fatalf("429 文案应说明当日磁盘配额，got %q", msg)
	}

	// 审计（超限即留痕）：audit_log 恰一条配额拒绝记录，归属本 robot。
	auditRows, err := f.db.QueryDump(context.Background(),
		`SELECT subject FROM audit_log WHERE kind = ?`, auditKindOpenAPI+"_file_upload_quota_rejected")
	if err != nil {
		t.Fatalf("查询审计：%v", err)
	}
	if len(auditRows) != 1 || auditRows[0][0] != f.robot {
		t.Fatalf("超配额拒绝应落 1 条审计且归属本 robot，got %v", auditRows)
	}

	// 拒绝路径不留孤儿字节：本 robot 元数据行仍为 2（被拒文件已删，未计入配额）。
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT path FROM openapi_files WHERE robot_id = ? ORDER BY created_ms`, f.robot)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("被拒上传不应落元数据行，want 2 got %d", len(rows))
	}

	// robot 隔离：另一 robot 从零计账，仍可上传（本面 token↔robotId 归属核对，
	// 须为 robot-b 签发自有令牌——与 clientlog 的无鉴权面不同）。
	tokB, err := f.api.IssueOpenAPIToken(context.Background(), "robot-quota-b2", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	st, _ = fileUploadTok(t, f, "robot-quota-b2", tokB, []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("另一 robot 不受配额影响应 200，got %d", st)
	}

	// 现存文件口径：清理本 robot 一个现存落盘文件 → 占用回落 20B → 再传 20B 可通过。
	if err := os.Remove(filepath.Clean(rows[0][0])); err != nil {
		t.Fatalf("清理现存文件：%v", err)
	}
	st, env = fileUpload(t, f, f.robot, []byte(blob20))
	if st != http.StatusOK {
		t.Fatalf("清理现存文件释放配额后应 200（20 现存+20 新=40 ≤ 48），got %d %v", st, env)
	}
}
