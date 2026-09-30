package panel

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAssetCacheRevalidation 面板静态资源必须「每次回源校验」：带 ETag + Cache-Control: no-cache，
// 指纹命中回 304、陈旧指纹回全文。
//
// 为什么钉这个：面板资源是 go:embed 打进二进制的，部署即整包替换；此前这两个响应完全没有
// 缓存头，浏览器按启发式缓存留住旧副本，于是前端改动上线后用户看到的还是旧界面（本项目已两次
// 踩到，表现为"部署了但没看见更新"）。这条测试保证部署后普通刷新即可看到新版。
func TestAssetCacheRevalidation(t *testing.T) {
	p := newTestPanel()
	for _, path := range []string{"/panel/", "/panel/app.js"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d want 200", path, rec.Code)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: 缺 ETag（浏览器无法判断版本）", path)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("%s: Cache-Control=%q，需含 no-cache 才会回源校验", path, cc)
		}
		body := rec.Body.String()
		if body == "" {
			t.Fatalf("%s: 首次响应不应为空", path)
		}

		// 同指纹再取 → 304 且不带 body（省钱路径），安全头照常。
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("If-None-Match", etag)
		rec304 := httptest.NewRecorder()
		p.ServeHTTP(rec304, req)
		if rec304.Code != http.StatusNotModified {
			t.Errorf("%s: 同 ETag 应回 304，实际 %d", path, rec304.Code)
		}
		if rec304.Body.Len() != 0 {
			t.Errorf("%s: 304 不应带 body（实际 %d 字节）", path, rec304.Body.Len())
		}
		if rec304.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: 304 也要带安全头", path)
		}

		// 陈旧指纹（= 部署换过内容）→ 必须回全文，绝不能误判 304 让用户继续跑旧界面。
		reqStale := httptest.NewRequest("GET", path, nil)
		reqStale.Header.Set("If-None-Match", `"stale-fingerprint"`)
		recStale := httptest.NewRecorder()
		p.ServeHTTP(recStale, reqStale)
		if recStale.Code != http.StatusOK || recStale.Body.String() != body {
			t.Errorf("%s: 陈旧 ETag 必须回全文，实际 code=%d len=%d want %d",
				path, recStale.Code, recStale.Body.Len(), len(body))
		}
	}
}

// TestAssetETagsDiffer 两个资源的指纹必须不同（否则 304 判断会把页面与脚本混为一谈）。
func TestAssetETagsDiffer(t *testing.T) {
	if indexETag == appETag {
		t.Fatalf("index 与 app.js 指纹相同（%s）：资源指纹必须按内容区分", indexETag)
	}
	if !strings.HasPrefix(indexETag, `"`) || !strings.HasSuffix(indexETag, `"`) {
		t.Errorf("ETag 需带引号（HTTP 规范），实际 %q", indexETag)
	}
}

// TestAssetsGzip 静态资源的压缩协商：接受 gzip 时下发预压缩副本（同 ETag、
// 带 Vary），解压后与原文逐字节一致；`gzip;q=0` 的显式拒绝必须回落原文；
// 304 校验路径与编码无关（同 ETag 仍回 304）。
//
// 动机：两份资源未压缩共约 400KB，经隧道/公网每次冷加载全量传输；
// 预压缩后约 130KB。压错了（如把 q=0 也压）表现是客户端乱码白屏，
// 而本地 curl 一把梭测不出来——所以用测试钉住协商细节。
func TestAssetsGzip(t *testing.T) {
	p := newTestPanel()
	for _, path := range []string{"/panel/", "/panel/app.js"} {
		recRaw := httptest.NewRecorder()
		p.ServeHTTP(recRaw, httptest.NewRequest("GET", path, nil))
		raw := recRaw.Body.Bytes()
		if len(raw) == 0 {
			t.Fatalf("%s: 原文为空", path)
		}

		// 1) 接受 gzip → 压缩副本，解压后与原文一致
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		recGz := httptest.NewRecorder()
		p.ServeHTTP(recGz, req)
		if recGz.Code != http.StatusOK {
			t.Fatalf("%s: gzip 请求 code=%d want 200", path, recGz.Code)
		}
		if got := recGz.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("%s: Content-Encoding=%q want gzip", path, got)
		}
		if v := recGz.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
			t.Errorf("%s: Vary=%q 需含 Accept-Encoding", path, v)
		}
		if recGz.Header().Get("ETag") != recRaw.Header().Get("ETag") {
			t.Errorf("%s: 压缩与原文应共用同一 ETag（编码由 Vary 协商）", path)
		}
		zr, err := gzip.NewReader(bytes.NewReader(recGz.Body.Bytes()))
		if err != nil {
			t.Fatalf("%s: 响应不是合法 gzip: %v", path, err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("%s: 解压失败: %v", path, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("%s: 解压内容与原文不一致（got %d bytes want %d）", path, len(got), len(raw))
		}
		if recGz.Body.Len() >= len(raw) {
			t.Errorf("%s: 压缩后 %d 字节未小于原文 %d 字节", path, recGz.Body.Len(), len(raw))
		}

		// 2) gzip;q=0（明确拒绝）→ 必须回原文，不能压
		reqQ0 := httptest.NewRequest("GET", path, nil)
		reqQ0.Header.Set("Accept-Encoding", "gzip;q=0")
		recQ0 := httptest.NewRecorder()
		p.ServeHTTP(recQ0, reqQ0)
		if enc := recQ0.Header().Get("Content-Encoding"); enc != "" {
			t.Errorf("%s: q=0 时不应压缩，实际 Content-Encoding=%q", path, enc)
		}
		if !bytes.Equal(recQ0.Body.Bytes(), raw) {
			t.Errorf("%s: q=0 时应回原文", path)
		}

		// 3) 304 路径与编码无关
		req304 := httptest.NewRequest("GET", path, nil)
		req304.Header.Set("Accept-Encoding", "gzip")
		req304.Header.Set("If-None-Match", recRaw.Header().Get("ETag"))
		rec304 := httptest.NewRecorder()
		p.ServeHTTP(rec304, req304)
		if rec304.Code != http.StatusNotModified || rec304.Body.Len() != 0 {
			t.Errorf("%s: 带 gzip 协商的 304 校验失败 code=%d len=%d", path, rec304.Code, rec304.Body.Len())
		}
	}
}
