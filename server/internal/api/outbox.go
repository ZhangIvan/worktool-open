// 回调 outbox worker（docs/02-ARCHITECTURE.md §4 api 行；FR-35）：
// 结果落库同事务 enqueue（scheduler 侧已保证）→ 本 worker 投递：
//   - HMAC-SHA256 签名（X-WT-Signature: hex(HMAC(secret, "ts." + body))，防重放时间戳）；
//   - 重试 1s/2s/4s（默认），第 4 次尝试失败 → 死信（status=dead + 审计）；
//   - 默认禁外网：回调 URL 仅允许 loopback 接收器（SR-09 同型断言），
//     显式 allowNonLoopback=true 才放开（部署侧须 TLS，SR-01）。
package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

// DefaultRetryDelays 默认重试间隔（1/2/4s；首次立即，共 4 次尝试，第 4 次失败死信）。
var DefaultRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// OutboxConfig 为回调分发配置。
type OutboxConfig struct {
	CallbackURL      string // 空 = 不投递（outbox 行保持 pending）
	CallbackSecret   string // HMAC 密钥
	AllowNonLoopback bool   // 默认 false：仅 loopback 接收器
	RetryDelays      []time.Duration
	PollInterval     time.Duration // 轮询间隔（默认 250ms）
	Client           *http.Client  // 测试注入
}

// OutboxWorker 为回调分发工作器。
type OutboxWorker struct {
	db     *sqlite.DB
	cfg    OutboxConfig
	client *http.Client
	now    func() time.Time
	log    interface{ Printf(string, ...any) }
}

// NewOutboxWorker 创建 worker；URL 非 loopback 且未显式放行 → 返回禁用错误
// （启动即拒绝式样，与 config SR-09 断言同型）。
func NewOutboxWorker(db *sqlite.DB, cfg OutboxConfig, logger interface{ Printf(string, ...any) }) (*OutboxWorker, error) {
	if cfg.RetryDelays == nil {
		cfg.RetryDelays = DefaultRetryDelays
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.CallbackURL != "" {
		u, err := url.Parse(cfg.CallbackURL)
		if err != nil {
			return nil, fmt.Errorf("api/outbox: 回调 URL 非法: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("api/outbox: 回调 URL 仅支持 http(s): %s", u.Scheme)
		}
		if !cfg.AllowNonLoopback && !isLoopbackHostURL(u) {
			return nil, fmt.Errorf("api/outbox: 默认禁外网——回调 URL %s 非 loopback 接收器；如确需外网须显式 allowNonLoopback（部署侧强制 TLS，SR-01/09）", u.Host)
		}
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &OutboxWorker{db: db, cfg: cfg, client: client, now: time.Now, log: logger}, nil
}

func isLoopbackHostURL(u *url.URL) bool {
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Sign 计算 X-WT-Signature：hex(HMAC-SHA256(secret, "<tsMilli>." + body))。
func Sign(secret string, tsMillis int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", tsMillis)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySign 供接收器校验（loopback 测试接收器复用）。
func VerifySign(secret string, tsMillis int64, body []byte, signatureHex string) bool {
	return hmac.Equal([]byte(Sign(secret, tsMillis, body)), []byte(signatureHex))
}

// Run 启动投递循环（周期 claim 待投递行 → POST → 状态机：delivered/retry/dead）。
func (w *OutboxWorker) Run(ctx context.Context) error {
	tk := time.NewTicker(w.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			if err := w.DeliverDue(ctx); err != nil {
				w.log.Printf("api/outbox: %v", err)
			}
		}
	}
}

// DeliverDue 投递一批到期行（幂等可重入；测试可直接调用）。
func (w *OutboxWorker) DeliverDue(ctx context.Context) error {
	if w.cfg.CallbackURL == "" {
		return nil // 未配置接收器：行保持 pending（禁外网缺省形态）
	}
	now := w.now().UTC()
	items, err := w.db.DueOutbox(ctx, now, 32)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := w.deliverOne(ctx, it, now); err != nil {
			return err
		}
	}
	return nil
}

func (w *OutboxWorker) deliverOne(ctx context.Context, it sqlite.OutboxItem, now time.Time) error {
	ts := now.UnixMilli()
	body := []byte(it.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WT-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-WT-Signature", Sign(w.cfg.CallbackSecret, ts, body))
	req.Header.Set("X-WT-Delivery-Attempt", strconv.Itoa(it.Attempts+1))

	resp, err := w.client.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
	delivered := err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300

	it.Attempts++
	it.UpdatedAt = now
	if delivered {
		it.Status = "delivered"
		it.LastError = ""
		return w.db.UpdateOutbox(ctx, it)
	}
	errMsg := "transport error"
	if err == nil {
		errMsg = fmt.Sprintf("http %d", resp.StatusCode)
	}
	it.LastError = errMsg
	if it.Attempts >= len(w.cfg.RetryDelays)+1 {
		// 第 4 次（=1 次首发 + 3 次重试）仍失败 → 死信 + 审计（docs/06 ⑤）
		it.Status = "dead"
		if err := w.db.UpdateOutbox(ctx, it); err != nil {
			return err
		}
		tx, txErr := w.db.Tx(ctx)
		if txErr == nil {
			_ = w.db.InsertAudit(ctx, tx, "outbox_dead_letter", it.CommandID,
				fmt.Sprintf("id=%d attempts=%d lastError=%s", it.ID, it.Attempts, errMsg), now)
			_ = tx.Commit()
		}
		return nil
	}
	delay := w.cfg.RetryDelays[it.Attempts-1]
	it.NextAttempt = now.Add(delay)
	return w.db.UpdateOutbox(ctx, it)
}

// ErrDisabled 为未配置回调时的显式语义（内部哨兵，不出现在公开 API）。
var ErrDisabled = errors.New("api/outbox: 未配置回调 URL")
