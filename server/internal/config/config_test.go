package config

import (
	"strings"
	"testing"
)

// TestValidateSR09 覆盖 SR-09 断言正反例：
// 正例（放行）= loopback 明文（本地原型例外）或 非 loopback + TLS；
// 反例（拒绝）= 非 loopback（含绑定所有接口形态）且未启用 TLS。
func TestValidateSR09(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool // true = 期望启动即拒绝
	}{
		// —— 正例：loopback + 明文，放行（默认形态）——
		{"loopback IPv4 明文放行", Config{Listen: "127.0.0.1:8080"}, false},
		{"loopback 127.0.0.2 放行", Config{Listen: "127.0.0.2:8080"}, false},
		{"loopback IPv6 明文放行", Config{Listen: "[::1]:9000"}, false},
		{"localhost 明文放行", Config{Listen: "localhost:8080"}, false},
		{"默认配置放行", Default(), false},
		// —— 正例：非 loopback 但已启用 TLS，放行 ——
		{"非 loopback + TLS 放行", Config{Listen: "192.0.2.10:8443", TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"}, false},
		{"通配绑定 + TLS 放行", Config{Listen: "0.0.0.0:8443", TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"}, false},
		{"主机名 + TLS 放行", Config{Listen: "wt.example.internal:8443", TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"}, false},
		// —— 反例：非 loopback 且未启用 TLS，启动即拒绝 ——
		{"通配 IPv4 明文拒绝", Config{Listen: "0.0.0.0:8080"}, true},
		{"空 host 绑定全接口明文拒绝", Config{Listen: ":8080"}, true},
		{"内网 IP 明文拒绝", Config{Listen: "192.0.2.10:8080"}, true},
		{"公网 IP 明文拒绝", Config{Listen: "203.0.113.10:8080"}, true},
		{"非 localhost 主机名明文拒绝", Config{Listen: "wt.example.internal:8080"}, true},
		{"IPv6 通配明文拒绝", Config{Listen: "[::]:8080"}, true},
		// —— 反例：TLS 配置不完整 / 地址非法，拒绝 ——
		{"仅证书无私钥拒绝", Config{Listen: "192.0.2.10:8443", TLSCertFile: "cert.pem"}, true},
		{"仅私钥无证书拒绝", Config{Listen: "192.0.2.10:8443", TLSKeyFile: "key.pem"}, true},
		{"监听地址缺端口拒绝", Config{Listen: "127.0.0.1"}, true},
		{"空监听地址拒绝", Config{Listen: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate(%+v) 期望返回错误（启动即拒绝），实际 nil", tc.cfg)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate(%+v) 期望放行，实际错误: %v", tc.cfg, err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "SR-09") && !strings.Contains(err.Error(), "tls") && !strings.Contains(err.Error(), "端口") {
				t.Fatalf("错误信息未指明原因: %v", err)
			}
		})
	}
}

// TestIsLoopbackHost 覆盖 host 判定的边界形态。
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"", false}, // ":8080" 绑定所有接口
		{"0.0.0.0", false},
		{"192.0.2.10", false},
		{"203.0.113.10", false},
		{"wt.example.internal", false},
		{"127.0.0.1.example.com", false}, // 伪 loopback 前缀域名不算
	}
	for _, tc := range cases {
		if got := IsLoopbackHost(tc.host); got != tc.want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestLoadPrecedence 验证 默认值 < 环境变量 < flag 的优先级。
func TestLoadPrecedence(t *testing.T) {
	// 默认值。
	cfg, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("Load(nil,nil) 失败: %v", err)
	}
	if cfg.Listen != DefaultListen {
		t.Fatalf("默认 Listen = %q, want %q", cfg.Listen, DefaultListen)
	}

	// 环境变量覆盖默认值（仍须 loopback，否则 SR-09 拒绝）。
	cfg, err = Load(nil, []string{"WT_LISTEN=127.0.0.1:19080"})
	if err != nil {
		t.Fatalf("Load(env) 失败: %v", err)
	}
	if cfg.Listen != "127.0.0.1:19080" {
		t.Fatalf("env 覆盖后 Listen = %q, want 127.0.0.1:19080", cfg.Listen)
	}

	// flag 覆盖环境变量。
	cfg, err = Load([]string{"-listen", "127.0.0.1:19081"}, []string{"WT_LISTEN=127.0.0.1:19080"})
	if err != nil {
		t.Fatalf("Load(flag over env) 失败: %v", err)
	}
	if cfg.Listen != "127.0.0.1:19081" {
		t.Fatalf("flag 覆盖后 Listen = %q, want 127.0.0.1:19081", cfg.Listen)
	}

	// 环境变量声明非 loopback 明文时，Load（=启动路径）即拒绝。
	if _, err := Load(nil, []string{"WT_LISTEN=0.0.0.0:8080"}); err == nil {
		t.Fatal("Load 应因 SR-09（env 非 loopback 明文）返回错误")
	}

	// 空环境变量值视为未设置，回落默认。
	cfg, err = Load(nil, []string{"WT_LISTEN="})
	if err != nil {
		t.Fatalf("Load(空 WT_LISTEN) 失败: %v", err)
	}
	if cfg.Listen != DefaultListen {
		t.Fatalf("空 env 值应回落默认，实际 Listen = %q", cfg.Listen)
	}
}

// TestEnableV1CompatDefaultOff（H-2，安全审计 TOP5）：v1 兼容 WS 面默认不挂载——
// 配置默认 false；flag -enable-v1-compat / env WT_ENABLE_V1_COMPAT 显式开启；
// flag 优先级高于 env（false 覆盖 env 的 1）。
func TestEnableV1CompatDefaultOff(t *testing.T) {
	cfg, err := Load(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableV1Compat {
		t.Fatal("EnableV1Compat 默认必须 false（v1 兼容面默认不挂载）")
	}

	cfg, err = Load([]string{"-enable-v1-compat", "-listen", "127.0.0.1:19095"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableV1Compat {
		t.Fatal("-enable-v1-compat 应开启 v1 兼容面")
	}

	cfg, err = Load(nil, []string{"WT_LISTEN=127.0.0.1:19095", "WT_ENABLE_V1_COMPAT=1"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableV1Compat {
		t.Fatal("WT_ENABLE_V1_COMPAT=1 应开启 v1 兼容面")
	}

	cfg, err = Load([]string{"-enable-v1-compat=false"}, []string{"WT_LISTEN=127.0.0.1:19095", "WT_ENABLE_V1_COMPAT=1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableV1Compat {
		t.Fatal("flag -enable-v1-compat=false 应覆盖 env 的 1（flag 优先）")
	}
}

// TestLoadT23Fields：T2.3 新增配置（DB/回调）装载与校验。
func TestLoadT23Fields(t *testing.T) {
	cfg, err := Load(nil, []string{
		"WT_LISTEN=127.0.0.1:19090",
		"WT_DB=/tmp/wt-test.db",
		"WT_CALLBACK_URL=http://127.0.0.1:9990/cb",
		"WT_CALLBACK_SECRET=s1",
		"WT_ALLOW_NONLOOPBACK_CALLBACK=1",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != "/tmp/wt-test.db" || cfg.CallbackURL != "http://127.0.0.1:9990/cb" ||
		cfg.CallbackSecret != "s1" || !cfg.AllowNonLoopbackCallback {
		t.Fatalf("T2.3 配置装载错误: %+v", cfg)
	}
	// 回调 URL 配置但密钥缺失 → 拒绝
	if _, err := Load(nil, []string{
		"WT_LISTEN=127.0.0.1:19090",
		"WT_CALLBACK_URL=http://127.0.0.1:9990/cb",
	}); err == nil {
		t.Fatal("callback-url 无 secret 应启动即拒绝")
	}
	// 空 WT_DB 回落默认
	cfg, err = Load(nil, []string{"WT_LISTEN=127.0.0.1:19090", "WT_DB="})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "worktool.db" {
		t.Fatalf("空 WT_DB 应回落默认，实际 %q", cfg.DBPath)
	}
	// 非 loopback 回调 + 明文绑定双违规均可被独立识别（SR-09 优先报监听）
	if _, err := Load(nil, []string{"WT_LISTEN=0.0.0.0:8080"}); err == nil {
		t.Fatal("SR-09 应拒绝")
	}
}
