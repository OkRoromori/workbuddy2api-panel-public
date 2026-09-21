package server

import (
	"net/http"
	"strings"
)

// paths.go 路径容错：接受客户端把 /v1 拼重复或漏掉的形式。
//
// 为什么需要：**客户端拼路径的方式彼此矛盾**。
//
//   - Codex 的 config.toml 要求 `base_url` **带** `/v1`（它自己再拼 `/responses`）；
//   - 而 cc-switch 的「Fetch Models」把 base 当**前缀**，再拼一次 `/v1/models`。
//
// 于是 base 填 `http://host/v1` 时，模型列表请求会变成 `http://host/v1/v1/models`
// → 404。cc-switch 仓库里正是这个问题（issue #6566「自动拉取模型列表不通」）。
//
// 用户在两边都没做错：Codex 的格式就这么要求的，cc-switch 的拼接规则也这么写的。
// 所以**在网关侧兜住**：把重复的 `/v1` 折叠、补上漏掉的 `/v1`、去掉尾斜杠。
//
// 为什么只对「已知端点」做，而不是无条件重写：
// 无条件重写会把真正的 404 也吞掉——用户写错路径时将得不到任何提示，
// 那比多写几行映射更糟。这里只认网关确实提供的那几条路径。
// 注意：**只列 /v1 下面的端点**。`/status`、`/healthz`、`/panel/` 都是顶层路由，
// 把 `/status` 放进来会被"补 /v1"的规则改成 `/v1/status`，反而把好路由打断
//（第一次写时就踩了：TestStatusEndpoint 等四个测试全红）。
var knownEndpoints = []string{
	"/chat/completions",
	"/responses",
	"/messages",
	"/models",
}

// normalizePath 把常见的前缀错误折叠成网关真正注册的路径。
// 认不出来就原样返回（让它正常 404）。
func normalizePath(p string) string {
	if p == "" {
		return p
	}
	orig := p
	// 去掉尾斜杠（/v1/models/ → /v1/models）
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	// 折叠重复的 /v1：/v1/v1/models → /v1/models（可能叠多次）
	for strings.Contains(p, "/v1/v1") {
		p = strings.ReplaceAll(p, "/v1/v1", "/v1")
	}
	// 已是完整路径就直接用
	for _, e := range knownEndpoints {
		if p == "/v1"+e {
			return p
		}
	}
	// 漏了 /v1：/models → /v1/models
	for _, e := range knownEndpoints {
		if p == e {
			p = "/v1" + e
			return p
		}
	}
	// 认不出来 → 原样（保留 404 语义）
	return orig
}

// pathShim 路径容错中间件。必须在路由匹配**之前**改写（所以包在 mux 外层）。
func pathShim(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// /panel 相关一律不碰：它是面板自己的路由，不需要兼容客户端拼法。
		if !strings.HasPrefix(p, "/panel") {
			if np := normalizePath(p); np != p {
				r2 := r.Clone(r.Context())
				r2.URL.Path = np
				next.ServeHTTP(w, r2)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
