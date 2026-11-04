/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package api

import (
	"io"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"testing"
)

// TestClientLogUploadEndToEnd（W-M6 W2）：multipart 上传 → 200 + zip 落盘
// clientlogs/ + 审计行；缺 file part → 400。
func TestClientLogUploadEndToEnd(t *testing.T) {
	f := newOpenAPIFixture(t)

	var body bytes.Buffer
	w := multipartWriter(&body)
	_ = w.WriteField("robotId", "robot-openapi-01")
	_ = w.WriteField("clientVersion", "0.1.0-test")
	_ = w.WriteField("deviceInfo", "Android 12")
	part, _ := w.CreateFormFile("file", "log_2026_11_04.zip")
	part.Write([]byte("PK fake zip bytes"))
	w.Close()

	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/robot/clientLog/upload?robotId="+f.robot, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Idempotency-Key", "clog-e2e-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("upload = %d body=%s", resp.StatusCode, string(bodyBytes))
	}
	// 落盘核验（fileDir=fixture 临时目录）
	rows, _ := f.db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE kind='clientlog_upload'`)
	if len(rows) != 1 || rows[0][0] == "0" {
		t.Fatalf("应有审计行：%v", rows)
	}

	// 缺 file part → 400
	var body2 bytes.Buffer
	w2 := multipartWriter(&body2)
	_ = w2.WriteField("robotId", "robot-openapi-01")
	w2.Close()
	req2, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/robot/clientLog/upload?robotId="+f.robot, &body2)
	req2.Header.Set("Content-Type", w2.FormDataContentType())
	req2.Header.Set("Authorization", "Bearer "+f.token)
	req2.Header.Set("Idempotency-Key", "clog-e2e-2")
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2.StatusCode != 400 {
		t.Fatalf("缺 file part 应 400，实际 %d", resp2.StatusCode)
	}
	resp2.Body.Close()
}

func multipartWriter(b *bytes.Buffer) *multipart.Writer {
	mw := multipart.NewWriter(b)
	return mw
}
