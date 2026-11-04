// b2 端点补齐（REV-SRV-02；契约来源：docs/10 §B2「（server）fileUpload/upload +
// 回调配置组 4 端点」与 §D「fileUpload 文件/截图上传 → 自建文件 API」，上游
// apifox 页与 APK 实证语义由 parity 车道固化，字段面对齐 apifox 兼容面）：
//
//	POST /fileUpload/upload?robotId={id}   文件上传（multipart part 名 file；
//	                                      B2 交付=自建文件 API，docs/10 §D）
//	GET  /files/{fileId}                   上传文件取回（218 fileUrl 闭环：url 可被
//	                                      218 推送文件指令的 fileUrl 字段直接引用）
//	POST /robot/robotInfo/update?robotId={id} 机器人消息回调配置（QA 面：openCallback
//	                                      /replyAll/callbackUrl）
//
// 纪律：与兼容面扩展组同源——Bearer 鉴权（token 归属核对 robotId）+ 60QPM 限流
// （鉴权后计数，REV-SRV-04）+ 写操作强制 Idempotency-Key + 统一 {code,message,data}
// 信封 + 审计（SR-12）。文件落盘不进 SQLite（大对象走文件系统，元数据入库）。
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cn.worktool.open/server/internal/wire"
)

// openapiUploadMaxBytes 单文件上限 50MiB（b2 §1 契约：大小上限建议 ≤50M）。
const openapiUploadMaxBytes = 50 << 20

// openapiFileSchema 为上传文件元数据表（本包自治 schema，经 store 公共原语建表）。
const openapiFileSchema = `
CREATE TABLE IF NOT EXISTS openapi_files (
  file_id    TEXT PRIMARY KEY,
  robot_id   TEXT NOT NULL,
  name       TEXT NOT NULL,
  size       INTEGER NOT NULL,
  sha256     TEXT NOT NULL,
  path       TEXT NOT NULL,
  created_ms INTEGER NOT NULL
);`

// openapiRobotSettingsSchema 为机器人 QA 面设置表（b2 §2.1 robotInfo/update 持久化；
// openCallback/replyAll/callbackUrl 三字段）。
const openapiRobotSettingsSchema = `
CREATE TABLE IF NOT EXISTS openapi_robot_settings (
  robot_id     TEXT PRIMARY KEY,
  open_callback INTEGER NOT NULL,
  reply_all    TEXT NOT NULL,
  callback_url TEXT NOT NULL DEFAULT '',
  updated_ms   INTEGER NOT NULL
);`

// ensureOpenAPIUploadSchema 幂等建表（NewServeMux 装配时与 openapiCallbackSchema
// 同点调用）。
func (s *Server) ensureOpenAPIUploadSchema(ctx context.Context) error {
	for _, ddl := range []string{openapiFileSchema, openapiRobotSettingsSchema} {
		if _, err := s.DB.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("api/openapi: 建上传/设置表失败: %w", err)
		}
	}
	return nil
}

// fileDir 返回上传存储根目录（Server.FileDir 为空时默认 ./wt-files，随首个上传
// 创建；测试注入临时目录）。
func (s *Server) fileDir() string {
	if d := strings.TrimSpace(s.FileDir); d != "" {
		return d
	}
	return "wt-files"
}

// uploadGuard 上传/设置面统一前置（robotId 取自 query，b2 契约形态）：鉴权 →
// 60QPM 限流 → Idempotency-Key 强制。返回 false 时已写响应。
func (s *Server) uploadGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	robotID := strings.TrimSpace(r.URL.Query().Get("robotId"))
	if robotID == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeRobotMismatch, "缺 robotId（query ?robotId=，b2 契约必填；对齐上游 501011 机器人ID错误语义）")
		return "", false
	}
	if _, ae := s.authenticate(r.Context(), r, robotID); ae != nil {
		writeOpenAPIErr(w, ae.Status, openapiHTTPCode(ae), ae.Message)
		return "", false
	}
	if !s.openapiLimiter.allow(robotID, s.Now().UTC()) {
		writeOpenAPIErr(w, http.StatusTooManyRequests, openapiCodeRateLimited, "请求超出限流（60QPM）")
		return "", false
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiCodeIdemKeyRequired,
			"缺少 Idempotency-Key 请求头（写操作强制，spec §5.6.1；b2 冒烟样例同款）")
		return "", false
	}
	return robotID, true
}

// HandleFileUpload POST /fileUpload/upload?robotId=（multipart part 名 file）。
// 响应 data:{fileId,url,size,sha256}；url 为绝对地址（取自请求 Host/TLS 推断，
// 反向代理部署以 X-Forwarded-Proto 为准的改写属部署侧职责，登记于此）。
func (s *Server) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "POST", http.MethodPost) {
		return
	}
	robotID, ok := s.uploadGuard(w, r)
	if !ok {
		return
	}
	// 请求级兜底上限：part 上限 + 1MiB 余量（边界/其他 part）；超限由 multipart
	// 读取错误路径落 400，part 内超 50MiB 才是契约 413。
	r.Body = http.MaxBytesReader(w, r.Body, openapiUploadMaxBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "请求须为 multipart/form-data（part 名 file）")
		return
	}
	var part io.Reader
	var filename string
	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "multipart 解析失败: "+perr.Error())
			return
		}
		if p.FormName() != "file" {
			continue // 兼容多 part 顺序上传：跳过非 file part
		}
		part, filename = p, p.FileName()
		break
	}
	if part == nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "缺名为 file 的 multipart part（b2 契约 part 名=file）")
		return
	}
	// 流式落盘：fileId 为文件主键（能力 URL 依据），磁盘名与用户文件名解耦（防路径穿越）
	fileID := wire.NewUUID()
	dir := s.fileDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, "存储目录不可用: "+err.Error())
		return
	}
	dst := filepath.Join(dir, fileID)
	f, err := os.Create(dst)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(part, openapiUploadMaxBytes+1))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	if size == 0 {
		_ = os.Remove(dst)
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "文件为空（b2 负例：空文件 101011）")
		return
	}
	if size > openapiUploadMaxBytes {
		_ = os.Remove(dst)
		writeOpenAPIErr(w, http.StatusRequestEntityTooLarge, openapiUpstreamIllegalData,
			fmt.Sprintf("文件超上限（%d 字节 > %d；b2 契约 ≤50M）", size, openapiUploadMaxBytes))
		return
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	name := filepath.Base(strings.TrimSpace(filename)) // 仅取基名，去路径成分
	if name == "" || name == "." || name == "/" {
		name = fileID
	}
	now := s.Now().UTC()
	if _, err := s.DB.Exec(r.Context(), `
INSERT INTO openapi_files(file_id, robot_id, name, size, sha256, path, created_ms) VALUES (?,?,?,?,?,?,?)`,
		fileID, robotID, name, size, sum, dst, now.UnixMilli()); err != nil {
		_ = os.Remove(dst)
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	_ = s.DB.InsertAudit(r.Context(), nil, auditKindOpenAPI+"_file_upload", robotID,
		fmt.Sprintf("fileId=%s name=%s size=%d sha256=%s", fileID, name, size, sum), now)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{
		"fileId": fileID,
		"url":    scheme + "://" + r.Host + "/files/" + fileID,
		"size":   size,
		"sha256": sum,
	})
}

// HandleFileGet GET /files/{fileId}：上传文件取回（218 fileUrl 闭环的取数面）。
// 鉴权口径（登记）：**能力 URL**——fileId 为 128-bit 随机 UUID（不可枚举），不带
// Bearer；设备端（WS 会话主体，无 HTTP Bearer 凭据）据此直接拉取文件。若部署侧
// 需收紧，置于反代层加内网/签名约束（05 矩阵登记此取舍）。
func (s *Server) HandleFileGet(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "GET, HEAD", http.MethodGet, http.MethodHead) {
		return
	}
	fileID := r.PathValue("fileId")
	rows, err := s.DB.QueryDump(r.Context(),
		`SELECT name, path FROM openapi_files WHERE file_id = ?`, fileID)
	if err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	if len(rows) != 1 {
		writeOpenAPIErr(w, http.StatusNotFound, openapiCodeNotFound, "文件不存在")
		return
	}
	name, path := rows[0][0], rows[0][1]
	f, err := os.Open(path) //nolint:gosec // path 来自本服务落库的受控路径（非用户输入）
	if err != nil {
		writeOpenAPIErr(w, http.StatusNotFound, openapiCodeNotFound, "文件已缺失（元数据残留）")
		return
	}
	defer func() { _ = f.Close() }()
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(strings.ReplaceAll(name, `"`, ``), "\n", "")+`"`)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, time.Time{}, f)
}

// openapiRobotSettingsUpdateRequest b2 §2.1 请求体（指针判空区分「未提供」与 0/空串）。
type openapiRobotSettingsUpdateRequest struct {
	OpenCallback *int    `json:"openCallback"` // 必填：0 关 / 1 开
	ReplyAll     *string `json:"replyAll"`     // 必填：回复策略（取值集上游未公布，非空即可）
	CallbackURL  string  `json:"callbackUrl"`  // 选填：QA 回调 url
}

// HandleRobotInfoUpdate POST /robot/robotInfo/update?robotId=（b2 §2.1 QA 面消息
// 回调配置）。持久化 openapi_robot_settings；响应 {code:0,message:success}。
//
// 语义边界（如实登记，不夸大）：本端点落**配置事实**；openCallback=1 时新消息
// 推送到 callbackUrl 的实际分发属网关/事件车道（与回调组 type 5/6 同 dodDeferred
// 口径，openapi_rest.go 回调枚举注释同源）。callbackUrl 提供时受 SR-09 loopback
// 纪律约束（与回调绑定同款，AllowNonLoopbackCallbacks 显式放行例外）。
func (s *Server) HandleRobotInfoUpdate(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "POST", http.MethodPost) {
		return
	}
	robotID, ok := s.uploadGuard(w, r)
	if !ok {
		return
	}
	var req openapiRobotSettingsUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "请求体须为 JSON 对象 {openCallback, replyAll, callbackUrl}")
		return
	}
	if req.OpenCallback == nil || (*req.OpenCallback != 0 && *req.OpenCallback != 1) {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "openCallback 必填且须为 0（关）/1（开）")
		return
	}
	if req.ReplyAll == nil || strings.TrimSpace(*req.ReplyAll) == "" {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "replyAll 必填（回复策略，上游契约必填项）")
		return
	}
	if u := strings.TrimSpace(req.CallbackURL); u != "" {
		parsed, perr := url.Parse(u)
		if perr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "callbackUrl 须为合法 http(s) URL")
			return
		}
		if !s.AllowNonLoopbackCallbacks && !isLoopbackHostURL(parsed) {
			writeOpenAPIErr(w, http.StatusForbidden, openapiCodeNotWhitelisted,
				"默认禁外网：callbackUrl 仅允许 loopback 接收器（SR-09）；外网须部署侧显式放行并强制 TLS（SR-01）")
			return
		}
	}
	now := s.Now().UTC()
	if _, err := s.DB.Exec(r.Context(), `
INSERT INTO openapi_robot_settings(robot_id, open_callback, reply_all, callback_url, updated_ms)
VALUES (?,?,?,?,?)
ON CONFLICT(robot_id) DO UPDATE SET open_callback=excluded.open_callback,
 reply_all=excluded.reply_all, callback_url=excluded.callback_url, updated_ms=excluded.updated_ms`,
		robotID, *req.OpenCallback, strings.TrimSpace(*req.ReplyAll), strings.TrimSpace(req.CallbackURL), now.UnixMilli()); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	_ = s.DB.InsertAudit(r.Context(), nil, auditKindOpenAPI+"_robot_info_update", robotID,
		fmt.Sprintf("openCallback=%d replyAll=%s callbackUrl=%s", *req.OpenCallback, strings.TrimSpace(*req.ReplyAll), strings.TrimSpace(req.CallbackURL)), now)
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, nil)
}


// HandleClientLogUpload POST /robot/clientLog/upload（510 契约端点，W-M6 W2）：
// multipart form 字段 robotId/clientVersion/deviceInfo + 文件 part（名 file，
// 字段名上游未显式——按等价取 file）。zip 落盘 fileDir/clientlogs/（UUID 磁盘名
// 防穿越），元数据+审计入库。响应 {code:0,...}（端侧 LogUploadPort 只看 2xx）。
func (s *Server) HandleClientLogUpload(w http.ResponseWriter, r *http.Request) {
	if !methodGuard(w, r, "POST", http.MethodPost) {
		return
	}
	// 放宽前置（2026-11-04 会话 11 真机实证）：设备端 LogUploadPort 无 openapi
	// robot token（设备凭据=deviceToken，面不同），也无 Idempotency-Key——改用
	// IP 限流（60QPM 同池）+ form robotId（query 优先）。内网 TLS 部署下
	// 遥测面可接受；公网暴露属部署侧违规（SR-01/09）。
	robotID := strings.TrimSpace(r.URL.Query().Get("robotId"))
	if !s.openapiLimiter.allow(r.RemoteAddr, s.Now().UTC()) {
		writeOpenAPIErr(w, http.StatusTooManyRequests, openapiCodeRateLimited, "请求超出限流（60QPM/IP）")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, openapiUploadMaxBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "请求须为 multipart/form-data")
		return
	}
	var fileBuf bytes.Buffer
	var filename string
	form := map[string]string{}
	for {
		p, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "multipart 解析失败: "+perr.Error())
			return
		}
		if p.FormName() == "file" {
			// file 内容必须在本轮循环内立即读完——继续 NextPart() 会消费掉当前
			// part（2026-11-04 E2E 实证「上传内容为空」根因）。filename 留审计。
			filename = p.FileName()
			if _, err := io.Copy(&fileBuf, io.LimitReader(p, openapiUploadMaxBytes+1)); err != nil {
				writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "文件读取失败: "+err.Error())
				return
			}
			continue
		}
		if f := p.FormName(); f == "robotId" || f == "clientVersion" || f == "deviceInfo" {
			buf, _ := io.ReadAll(io.LimitReader(p, 256))
			form[f] = string(buf)
		}
	}
	_ = filename
	if fileBuf.Len() == 0 {
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "缺文件 part（名 file）或内容为空")
		return
	}
	if strings.TrimSpace(form["robotId"]) != "" && robotID == "" {
		robotID = strings.TrimSpace(form["robotId"])
	}
	fileID := wire.NewUUID()
	dir := filepath.Join(s.fileDir(), "clientlogs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, "存储目录不可用: "+err.Error())
		return
	}
	dst := filepath.Join(dir, fileID+".zip")
	size := int64(fileBuf.Len())
	if err := os.WriteFile(dst, fileBuf.Bytes(), 0o644); err != nil {
		writeOpenAPIErr(w, http.StatusInternalServerError, 101001, err.Error())
		return
	}
	if size > openapiUploadMaxBytes {
		_ = os.Remove(dst)
		writeOpenAPIErr(w, http.StatusBadRequest, openapiUpstreamIllegalData, "超过 50MiB 上限")
		return
	}
	now := s.Now().UTC()
	_ = s.DB.InsertAudit(ctxOf(r), nil, "clientlog_upload", robotID,
		fmt.Sprintf("fileId=%s size=%d clientVersion=%s deviceInfo=%s", fileID, size, form["clientVersion"], form["deviceInfo"]), now)
	writeOpenAPIData(w, http.StatusOK, openapiCodeSuccess, map[string]any{
		"fileId": fileID, "size": size, "robotId": robotID,
		"clientVersion": form["clientVersion"], "deviceInfo": form["deviceInfo"],
	})
}

// ctxOf 便捷取请求 ctx。
func ctxOf(r *http.Request) context.Context { return r.Context() }
