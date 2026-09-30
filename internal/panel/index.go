// index.go 面板静态资源与安全响应头。
//
// 资源经 go:embed 打进二进制（随服务部署，无外部构建步骤）：
//   - index.html  页面骨架
//   - app.js      全部前端逻辑（独立文件而非内联，为了启用无需 unsafe-inline 的严格 CSP）
//
// 安全头对"面板页面与全部 /panel/api/* 响应"统一生效：CSP 限制脚本只能来自本服务，
// 禁止被 iframe 嵌套（防点击劫持），禁 MIME 嗅探，并声明不泄露 Referer 出去。
package panel

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strings"
)

//go:embed index.html
var indexHTML []byte

//go:embed app.js
var appJS []byte

// 静态资源指纹与预压缩副本：embed 字节编译期即定，两者进程内只算一次。
// 指纹是「浏览器拿的是不是最新那份」的唯一判据；gzip 副本让每个请求零压缩成本。
var (
	indexETag = assetETag(indexHTML)
	appETag   = assetETag(appJS)
	indexGz   = gzipBytes(indexHTML)
	appGz     = gzipBytes(appJS)
)

// assetETag 内容指纹（sha256 前 16 字节 hex；足够抗碰撞，长度也短）。
func assetETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// gzipBytes 预压缩静态资源。面板两份资源未压缩共约 400KB，经隧道/公网每次
// 冷加载都要全量传输；gzip 后约 130KB（-68%）。失败返回 nil，调用方回落原文。
func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(b); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// acceptsGzip 客户端是否接受 gzip（识别 q=0 的显式拒绝）。
// 不用 strings.Contains 一刀切：`gzip;q=0` 是「明确不要 gzip」，
// 含子串就压会把不接受 gzip 的客户端喂成乱码。
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "gzip") {
			continue
		}
		return !strings.Contains(part, "q=0")
	}
	return false
}

// csp 内容安全策略（严格版，无需 unsafe-inline）：
//   - default-src 'none'        默认全禁，逐个开口
//   - script-src 'self'         只跑同源脚本（app.js）；页面无内联事件处理器/内联脚本
//   - style-src 'self' 'unsafe-inline'
//     style 的内联是设计取舍：页面有少量 style="..." 属性（进度条宽度、表格列宽），
//     允许内联样式不会导致脚本执行；仍禁止外部样式域与 @import 外链。
//   - connect-src 'self'        前端 fetch 只能打本服务
//   - img-src 'self' data:      图标/内联图
//   - form-action 'none'        页面无表单提交目标（配置页是 JS 提交）
//   - frame-ancestors 'none'    禁止被任何站点 iframe 嵌套（点击劫持）
//   - base-uri 'none'          禁止注入 <base> 改写相对路径
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; img-src 'self' data:; form-action 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// setSecurityHeaders 写入面板统一安全响应头（页面与 API 都要，API 也含 JSON 数据）。
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff") // 禁 MIME 嗅探
	w.Header().Set("X-Frame-Options", "DENY")           // 老浏览器兜底（CSP frame-ancestors 的等价项）
	w.Header().Set("Referrer-Policy", "no-referrer")    // 不外泄面板地址给外部站点
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

// serveAsset 输出静态资源，带「每次回源校验」的缓存语义 + 预压缩协商。
//
// 为什么要 ETag + no-cache：面板是 go:embed 资源，部署等于整包换二进制，但**浏览器不会
// 自己回源**——此前这两个响应没有任何缓存头，浏览器按启发式缓存留住旧副本，于是前端改动
// 上线后用户看到的还是旧界面（本项目已两次踩到：部署后"没看见更新啊"）。no-cache 要求
// 每次回源校验，指纹一致回 304；代价是本机回环的一次往返，换来「部署即所见」。
// 注意不能只写 no-store：那会连页面内的正常复用也丢掉，且无助于诊断"拿的是哪一版"。
//
// 压缩协商：ETag 跨编码复用（nginx 的常规做法）——ETag 标识资源版本，
// 编码差异由 Vary: Accept-Encoding 交给缓存层区分，no-cache 下无共享缓存风险。
func serveAsset(w http.ResponseWriter, r *http.Request, body, gz []byte, etag, ctype string) {
	setSecurityHeaders(w)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept-Encoding")
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", ctype)
	if len(gz) > 0 && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gz)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// index 输出面板页面（静态无秘密；数据接口 /panel/api/* 才走鉴权）。
func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	serveAsset(w, r, indexHTML, indexGz, indexETag, "text/html; charset=utf-8")
}

// appScript 输出前端逻辑（同源脚本，供 CSP script-src 'self' 加载）。
func (p *Panel) appScript(w http.ResponseWriter, r *http.Request) {
	serveAsset(w, r, appJS, appGz, appETag, "text/javascript; charset=utf-8")
}
