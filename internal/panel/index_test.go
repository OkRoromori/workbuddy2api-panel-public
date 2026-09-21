package panel

import (
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
