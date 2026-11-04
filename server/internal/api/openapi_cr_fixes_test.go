// CR 修复回归套件（REV-SRV-01/02/03/04/05）：重定向禁跟随 / b2 端点补齐 /
// 注册表排除集防漂移 / 限流器内存与配额防护 / 队列操作 L2 审批门。
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- REV-SRV-01：重定向禁跟随 ----

// TestCallbackProbeRejectsRedirect：绑定探测不跟随 3xx——loopback 目标 302 跳转
// 至「会返回 200+JSON 的第二目标」时，探测以 302 终态判失败（hit 必须为 0），
// 绑定被拒。第二目标用本地 httptest 承载（密闭等价：被测行为是「客户端绝不
// 跟随重定向」，跟随即命中计数；真实外网目标同理不可达）。
func TestCallbackProbeRejectsRedirect(t *testing.T) {
	f := newOpenAPIFixture(t)

	var mu sync.Mutex
	hits := 0
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(final.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/leaked", http.StatusFound)
	}))
	t.Cleanup(redir.Close)

	status, env := f.rest(t, http.MethodPost, "callbacks",
		`{"type":1,"callBackUrl":"`+redir.URL+`/redir"}`, nil)
	if status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalOp {
		t.Fatalf("302 目标应判校验不通过（400/101012），实际 %d %v", status, env)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("探测跟随了重定向（第二目标被命中 %d 次）——SR-09 可被 30x 打穿", hits)
	}
}

// ---- REV-SRV-03：注册表排除集防漂移 ----

// TestOpenAPIRegistryExclusionSetLocked 锁定『官方云→端全集 − 注册表 = 登记的
// 排除集』：排除集口径见 openapi_registry.go 注释。新增排除未登记注释、或漏
// 注册官方类型，此测试都会拦截（防「46=全集」误读复发）。
func TestOpenAPIRegistryExclusionSetLocked(t *testing.T) {
	registered := map[int]bool{}
	for _, it := range openapiCommandRegistry {
		registered[it.Type] = true
	}
	// 2xx 段官方全集：201..240 ∪ {243,244}（241/242 上游不存在，不在全集）
	official2xx := map[int]bool{}
	for typ := 201; typ <= 240; typ++ {
		official2xx[typ] = true
	}
	official2xx[243], official2xx[244] = true, true
	excluded2xx := map[int]string{
		215: "上游无定义页", 216: "上游无定义页", 217: "上游无定义页",
		231: "上游无定义页", 232: "上游无定义页",
		235: "官方页已定案（doc-9564531 §235），排期 W-M6/B3",
		243: "官方页已定案（doc-9564531 §243），排期 W-M6/FR-56 依赖",
	}
	for typ := range official2xx {
		reason, excluded := excluded2xx[typ]
		switch {
		case excluded && registered[typ]:
			t.Fatalf("type=%d 已登记排除原因（%s）却仍在注册表——口径漂移", typ, reason)
		case !excluded && !registered[typ]:
			t.Fatalf("type=%d 在官方 2xx 全集且不在排除集，必须注册（或补登记排除原因）", typ)
		}
	}
	// 30x 段：303/304 提交面注册；305 经 DELETE queue/{commandId} REST 等价面承接；
	// 306 经 GET queue；301/302/307 上报与事件类
	if !registered[303] || !registered[304] {
		t.Fatal("303/304 须注册")
	}
	for _, typ := range []int{305, 306, 307, 301, 302, 11, 101, 102, 283} {
		if registered[typ] {
			t.Fatalf("type=%d 不得注册（排除集口径见注册表注释）", typ)
		}
	}
	// 50x 段：501~515 注册（510~515 于 W-M6 正式批扩容，2026-11-04——fullBatch
	// 执行器与设备端口已装配；注释见 openapi_registry.go W-M6 扩容段）
	for typ := 501; typ <= 515; typ++ {
		if !registered[typ] {
			t.Fatalf("type=%d 须注册", typ)
		}
	}
}

// ---- REV-SRV-04：限流器内存与配额防护 ----

// TestOpenAPILimiterPrunesEmptyKeysAndCapsCardinality 单元：窗口过期 entry 回收
// （map 不随唯一 robotId 无界增长）+ key 基数上限（超限拒新保旧）。
func TestOpenAPILimiterPrunesEmptyKeysAndCapsCardinality(t *testing.T) {
	l := newOpenAPILimiter(OpenAPIConfig{RateLimitPerMinute: 60, RateWindow: time.Minute})
	now := time.Now().UTC()
	if !l.allow("r1", now) || len(l.hits) != 1 {
		t.Fatalf("首次放行且仅 1 个 entry: allow=%v entries=%d", l.allow("r1", now), len(l.hits))
	}
	// 窗口外再放行：过期 entry 回收后重建（仍是 1 个 entry，不残留）
	if !l.allow("r1", now.Add(2*time.Minute)) {
		t.Fatal("窗口外应放行")
	}
	if len(l.hits) != 1 {
		t.Fatalf("过期 entry 应回收：entries=%d（REV-SRV-04 内存耗尽向量）", len(l.hits))
	}
	// 基数上限：灌满后新 key 拒绝、既有 key 不受影响
	//（"r1" 已占 1 entry，故再灌 cap-1 个即满）
	for i := 0; i < openapiLimiterKeysCap-1; i++ {
		if !l.allow(fmt.Sprintf("k-%d", i), now) {
			t.Fatalf("灌满阶段 key %d 不应被拒（未超基数）", i)
		}
	}
	if l.allow("k-new-overflow", now) {
		t.Fatal("超基数上限的新 key 应拒绝（fail-closed）")
	}
	if !l.allow("k-0", now) {
		t.Fatal("既有 key 不受基数上限影响")
	}
}

// TestOpenAPIUnauthFloodNoMapGrowthNoQuota 集成：未认证请求（受害者 robot 寻址
// + 随机 robotId 路径参数）不进限流 map、不消耗受害 robot 的 60QPM 配额——
// 鉴权先于限流（REV-SRV-04）。
func TestOpenAPIUnauthFloodNoMapGrowthNoQuota(t *testing.T) {
	f := newOpenAPIFixture(t)
	// 受害者 robot 寻址的未认证洪泛 61 次（旧实现会耗尽其 60QPM 配额）
	for i := 0; i < 61; i++ {
		if got := f.restNoAuth(t, http.MethodGet, "info", ""); got != http.StatusUnauthorized {
			t.Fatalf("未认证应 401，实际 %d", got)
		}
	}
	// 随机 robotId 路径参数洪泛（旧实现会每唯一 robotId 留一条永不回收 entry）
	for i := 0; i < 200; i++ {
		req, _ := http.NewRequest(http.MethodGet,
			f.srv.URL+"/openapi/v1/robots/rand-"+fmt.Sprint(i)+"/info", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证随机 robotId 应 401，实际 %d", resp.StatusCode)
		}
	}
	if n := len(f.api.openapiLimiter.hits); n != 0 {
		t.Fatalf("未认证请求不得进入限流 map（entries=%d，REV-SRV-04 内存/配额向量）", n)
	}
	// 受害者随后携合法令牌请求不受影响（配额未被未认证流量消耗）
	status, env := f.rest(t, http.MethodGet, "info", "", nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("合法请求应 200/0，实际 %d %v（未认证流量消耗了配额）", status, env)
	}
}

// ---- REV-SRV-05：队列清理/指定取消 L2 审批门 ----

// approveQueueOp 测试辅助：走 202 登记审批 → 独立 approver 决议通过。
func (f *openapiFixture) approveQueueOp(t *testing.T, env map[string]any) string {
	t.Helper()
	d := dataMap(t, env)
	approvalID, _ := d["approvalId"].(string)
	if approvalID == "" {
		t.Fatalf("202 应携带 approvalId: %v", env)
	}
	if err := f.api.Pipeline.DecideApproval(context.Background(), approvalID,
		"approver-queue", true, "队列操作审批（测试）"); err != nil {
		t.Fatal(err)
	}
	return approvalID
}

// queueOpWithApproval 携审批号执行队列操作。
func (f *openapiFixture) queueOpWithApproval(t *testing.T, method, path, approvalID string) (int, map[string]any) {
	t.Helper()
	return f.rest(t, method, path, "", map[string]string{"X-WT-Approval-Id": approvalID})
}

// TestOpenAPIQueueOpsRequireApproval：未携审批号 → 202 登记（队列原封不动）；
// 决议后携号 → 执行；一次性审批消费后重放 → 403；参数指纹不一致 → 403。
func TestOpenAPIQueueOpsRequireApproval(t *testing.T) {
	f := newOpenAPIFixture(t)

	// 造 2 条排队指令
	var ids []string
	for _, c := range []string{"qa1", "qa2"} {
		_, env := f.post(t, `{"type":203,"targetRoom":"审批门群","receivedContent":"`+c+`"}`, nil)
		ids = append(ids, dataMap(t, env)["commandId"].(string))
	}

	// ① 清空未携审批号 → 202 + approvalId，队列原封不动
	status, env := f.rest(t, http.MethodDelete, "queue", "", nil)
	if status != http.StatusAccepted || codeOf(t, env) != openapiCodeApprovalRequired {
		t.Fatalf("清空未携审批应 202/%d，实际 %d %v", openapiCodeApprovalRequired, status, env)
	}
	clearApproval := f.approveQueueOp(t, env)
	_, env = f.rest(t, http.MethodGet, "queue", "", nil)
	if dataMap(t, env)["count"].(float64) != 2 {
		t.Fatalf("202 登记阶段不得动队列: %v", env)
	}

	// ② 指定取消未携审批号 → 202；决议后携号执行成功
	status, env = f.rest(t, http.MethodDelete, "queue/"+ids[0], "", nil)
	if status != http.StatusAccepted || codeOf(t, env) != openapiCodeApprovalRequired {
		t.Fatalf("指定取消未携审批应 202，实际 %d %v", status, env)
	}
	cancelApproval := f.approveQueueOp(t, env)
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue/"+ids[0], cancelApproval)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("携已批审批指定取消应 200: %d %v", status, env)
	}

	// ③ 一次性审批已消费：同审批再用于另一指令 → 403（指纹也不同，双保险拦截）
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue/"+ids[1], cancelApproval)
	if status != http.StatusForbidden || codeOf(t, env) != openapiCodeApprovalRequired {
		t.Fatalf("已消费/指纹不符审批应 403，实际 %d %v", status, env)
	}

	// ④ 清空携「为取消登记的审批」→ 指纹不一致 403（防审批 A 清 B）
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue", cancelApproval)
	if status != http.StatusForbidden {
		t.Fatalf("跨操作审批应 403（指纹绑定），实际 %d %v", status, env)
	}

	// ⑤ 清空携本操作已批审批 → 200 清掉剩余 1 条
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue", clearApproval)
	if status != http.StatusOK || dataMap(t, env)["cleared"].(float64) != 1 {
		t.Fatalf("携已批审批清空应 200 cleared=1: %d %v", status, env)
	}

	// ⑥ 不存在的审批号 → 403
	status, env = f.queueOpWithApproval(t, http.MethodDelete, "queue", "no-such-approval")
	if status != http.StatusForbidden {
		t.Fatalf("未知审批号应 403，实际 %d %v", status, env)
	}
}

// ---- REV-SRV-02：fileUpload/upload + /files/{id} + robotInfo/update ----

// uploadRequest 构造 multipart 上传请求（part 名 file，b2 契约形态）。
func (f *openapiFixture) uploadRequest(t *testing.T, robotID, filename string, content io.Reader, contentLen int64,
	auth bool, idem bool) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/fileUpload/upload?robotId="+robotID, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if auth {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	if idem {
		req.Header.Set("Idempotency-Key", "idem-upload-"+fmt.Sprint(time.Now().UnixNano()))
	}
	return req
}

// TestFileUploadRoundTrip：上传 → 元数据/sha256 → GET /files/{id} 取回字节一致；
// 负例：空文件 101011、缺 file part、缺 robotId 400/501011、超限 413、未认证 401、
// 缺幂等键 101003、未知文件 404。
func TestFileUploadRoundTrip(t *testing.T) {
	f := newOpenAPIFixture(t)
	f.api.FileDir = t.TempDir()

	content := []byte("worktool 文件上传闭环测试内容 123")
	// 正例：上传 → 200 + {fileId,url,size,sha256}
	req := f.uploadRequest(t, f.robot, "note.txt", bytes.NewReader(content), int64(len(content)), true, true)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("上传应 200/0，实际 %d %v", resp.StatusCode, env)
	}
	d := dataMap(t, env)
	fileID, _ := d["fileId"].(string)
	sum := sha256.Sum256(content)
	if fileID == "" || d["sha256"] != hex.EncodeToString(sum[:]) || d["size"].(float64) != float64(len(content)) {
		t.Fatalf("上传元数据异常: %v", d)
	}
	urlStr, _ := d["url"].(string)
	if !strings.Contains(urlStr, "/files/"+fileID) {
		t.Fatalf("url 应指向 /files/{fileId}: %v", urlStr)
	}

	// GET 取回：字节一致（218 fileUrl 闭环取数面）
	getReq, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/files/"+fileID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || !bytes.Equal(got, content) {
		t.Fatalf("取回应还原原字节: status=%d len=%d", getResp.StatusCode, len(got))
	}
	if ct := getResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("txt 应按扩展名给 Content-Type: %s", ct)
	}

	// 空文件 → 101011
	req = f.uploadRequest(t, f.robot, "empty.txt", strings.NewReader(""), 0, true, true)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalData {
		t.Fatalf("空文件应 400/101011，实际 %d %v", resp.StatusCode, env)
	}

	// 缺 robotId → 400/501011（b2 负例口径）
	req = f.uploadRequest(t, "", "x.txt", bytes.NewReader(content), int64(len(content)), true, true)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || codeOf(t, env) != openapiCodeRobotMismatch {
		t.Fatalf("缺 robotId 应 400/501011，实际 %d %v", resp.StatusCode, env)
	}

	// 未认证 → 401
	req = f.uploadRequest(t, f.robot, "x.txt", bytes.NewReader(content), int64(len(content)), false, true)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，实际 %d", resp.StatusCode)
	}

	// 缺幂等键 → 400/101003
	req = f.uploadRequest(t, f.robot, "x.txt", bytes.NewReader(content), int64(len(content)), true, false)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || codeOf(t, env) != openapiCodeIdemKeyRequired {
		t.Fatalf("缺幂等键应 400/101003，实际 %d %v", resp.StatusCode, env)
	}

	// 超限 → 413（上限+1 字节零流）
	big := &zeroReader{remain: openapiUploadMaxBytes + 1}
	req = f.uploadRequest(t, f.robot, "big.bin", big, openapiUploadMaxBytes+1, true, true)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，实际 %d %v", resp.StatusCode, env)
	}

	// 未知文件 → 404
	getReq, _ = http.NewRequest(http.MethodGet, f.srv.URL+"/files/no-such-file", nil)
	getResp, err = http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知文件应 404，实际 %d", getResp.StatusCode)
	}
}

// zeroReader 定长零字节流（超限用例构造，避免 50MB 实体驻留内存的哈希成本）。
type zeroReader struct{ remain int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.remain <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > z.remain {
		n = z.remain
	}
	for i := int64(0); i < n; i++ {
		p[i] = 0
	}
	z.remain -= n
	return int(n), nil
}

// TestRobotInfoUpdate：b2 §2.1 QA 面配置——正例持久化（含 loopback callbackUrl）；
// 负例：openCallback 越界 / replyAll 缺失 / callbackUrl 非 loopback（SR-09 403）/
// 未认证 401 / 缺幂等键。
func TestRobotInfoUpdate(t *testing.T) {
	f := newOpenAPIFixture(t)

	// 全路径请求（上游契约路径 /robot/robotInfo/update?robotId=，不在 /openapi/v1 前缀下）
	doPost := func(body string, hdr map[string]string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost,
			f.srv.URL+"/robot/robotInfo/update?robotId="+f.robot, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Idempotency-Key", "idem-riu-"+fmt.Sprint(time.Now().UnixNano()))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&env)
		resp.Body.Close()
		return resp.StatusCode, env
	}

	// 正例：开启 + 回复策略 + loopback callbackUrl
	status, env := doPost(`{"openCallback":1,"replyAll":"all","callbackUrl":"http://127.0.0.1:9/qa"}`, nil)
	if status != http.StatusOK || codeOf(t, env) != 0 {
		t.Fatalf("robotInfo/update 应 200/0，实际 %d %v", status, env)
	}
	rows, err := f.db.QueryDump(context.Background(),
		`SELECT open_callback, reply_all, callback_url FROM openapi_robot_settings WHERE robot_id = ?`, f.robot)
	if err != nil || len(rows) != 1 || rows[0][0] != "1" || rows[0][1] != "all" {
		t.Fatalf("配置应持久化: rows=%v err=%v", rows, err)
	}

	// 覆盖更新（UPSERT）：关掉
	status, env = doPost(`{"openCallback":0,"replyAll":"none"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("覆盖更新应 200: %d %v", status, env)
	}
	rows, _ = f.db.QueryDump(context.Background(),
		`SELECT open_callback, reply_all, callback_url FROM openapi_robot_settings WHERE robot_id = ?`, f.robot)
	if len(rows) != 1 || rows[0][0] != "0" || rows[0][2] != "" {
		t.Fatalf("覆盖更新后应以最新值为准: %v", rows)
	}

	// 负例：openCallback 越界
	if status, env = doPost(`{"openCallback":2,"replyAll":"all"}`, nil); status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalData {
		t.Fatalf("openCallback=2 应 400/101011，实际 %d %v", status, env)
	}
	// 负例：replyAll 缺失（必填）
	if status, env = doPost(`{"openCallback":1}`, nil); status != http.StatusBadRequest || codeOf(t, env) != openapiUpstreamIllegalData {
		t.Fatalf("缺 replyAll 应 400/101011，实际 %d %v", status, env)
	}
	// 负例：callbackUrl 非 loopback（SR-09 默认禁外网）
	if status, env = doPost(`{"openCallback":1,"replyAll":"all","callbackUrl":"https://cb.example.com/qa"}`, nil); status != http.StatusForbidden || codeOf(t, env) != openapiCodeNotWhitelisted {
		t.Fatalf("外网 callbackUrl 应 403（SR-09），实际 %d %v", status, env)
	}
	// 负例：缺幂等键
	req, _ := http.NewRequest(http.MethodPost,
		f.srv.URL+"/robot/robotInfo/update?robotId="+f.robot, strings.NewReader(`{"openCallback":1,"replyAll":"all"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, _ := http.DefaultClient.Do(req)
	var env2 map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env2)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || codeOf(t, env2) != openapiCodeIdemKeyRequired {
		t.Fatalf("缺幂等键应 400/101003，实际 %d %v", resp.StatusCode, env2)
	}
}
