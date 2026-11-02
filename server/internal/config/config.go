// Package config 负责服务端启动配置的装载与校验（任务卡 T1.3）。
//
// 配置来源与优先级：内置默认值 < 环境变量（WT_*）< 命令行 flag。
//
// SR-09 断言（docs/01 需求 / docs/02-ARCHITECTURE.md §4 cmd/server 行 / docs/06 §3 ⑦）：
// 绑定地址非 loopback 且未启用 TLS 时，启动即拒绝（Validate 返回错误），
// 禁止明文监听对外地址；loopback 明文仅作为本地原型例外。
package config

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
)

// DefaultListen 为默认监听地址：仅 loopback（SR-09 默认形态）。
const DefaultListen = "127.0.0.1:8080"

// 环境变量名（空值视为未设置）。
const (
	EnvListen  = "WT_LISTEN"
	EnvTLSCert = "WT_TLS_CERT"
	EnvTLSKey  = "WT_TLS_KEY"
)

// Config 为服务端启动配置。
type Config struct {
	// Listen 为 host:port 形式的监听地址。
	Listen string
	// TLSCertFile / TLSKeyFile 同时非空时视为启用 TLS。
	TLSCertFile string
	TLSKeyFile  string
}

// Default 返回默认配置：loopback + 明文（本地原型例外，见 SR-09）。
func Default() Config {
	return Config{Listen: DefaultListen}
}

// TLSEnabled 报告是否启用 TLS（证书与私钥须同时提供）。
func (c Config) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// Load 依次应用默认值、环境变量 environ（"KEY=VALUE" 形式）与命令行参数 argv，
// 最后执行 Validate（含 SR-09 断言），不合法即返回错误，由调用方启动即退出。
func Load(argv []string, environ []string) (Config, error) {
	cfg := Default()
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		switch k {
		case EnvListen:
			cfg.Listen = v
		case EnvTLSCert:
			cfg.TLSCertFile = v
		case EnvTLSKey:
			cfg.TLSKeyFile = v
		}
	}
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "host:port 监听地址（默认 "+DefaultListen+"）")
	fs.StringVar(&cfg.TLSCertFile, "tls-cert", cfg.TLSCertFile, "TLS 证书 PEM 路径；与 -tls-key 同时设置即启用 TLS")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key", cfg.TLSKeyFile, "TLS 私钥 PEM 路径；与 -tls-cert 同时设置即启用 TLS")
	if err := fs.Parse(argv); err != nil {
		return Config{}, fmt.Errorf("config: 解析命令行参数失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校验配置，包含 SR-09 断言：非 loopback 绑定且未启用 TLS → 拒绝。
func (c Config) Validate() error {
	certEmpty, keyEmpty := c.TLSCertFile == "", c.TLSKeyFile == ""
	if certEmpty != keyEmpty {
		return fmt.Errorf("config: -tls-cert 与 -tls-key 必须同时设置")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("config: 监听地址 %q 缺少端口或格式非法: %w", c.Listen, err)
	}
	if !c.TLSEnabled() && !IsLoopbackHost(host) {
		return fmt.Errorf(
			"config: SR-09 违规：监听 %q 非 loopback 且未启用 TLS，明文对外绑定启动即拒绝；请改绑 127.0.0.1/localhost/::1 或同时提供 -tls-cert 与 -tls-key",
			c.Listen,
		)
	}
	return nil
}

// IsLoopbackHost 判定 host 是否 loopback：文本 "localhost"、IPv4 127.0.0.0/8、IPv6 ::1。
// 空 host（如 ":8080"，实际绑定所有接口）与任何其他 IP/主机名均视为非 loopback。
func IsLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
