// Package api 提供服务端 REST 兼容层与回调分发（docs/02-ARCHITECTURE.md §4）。
//
// 计划职责（T3.2 落地）：
//   - REST v1 / apifox 兼容原生支持（FR-34）：/webserver/wework/{robotId} 端点、
//     WeworkMessageListBean 信封、201~234/501~509 数字指令目录、0/10xxx/20xxx/50xxx 错误码；
//   - 回调分发（FR-35）：HMAC-SHA256 签名 + 有界重试 + 死信，回调 outbox 与结果同事务
//     （docs/06 §3 ⑤ 吸收清单）。
//
// 本骨架（T1.3）仅挂载 /healthz 存活探针；业务路由随 T2.3/T3.2 逐步挂载到本包。
package api

import "net/http"

// NewServeMux 构建服务端 HTTP 路由。
// 骨架阶段仅含 /healthz（存活探针）；就绪探针（依赖 store/网关状态）随 T2.3 引入。
func NewServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
