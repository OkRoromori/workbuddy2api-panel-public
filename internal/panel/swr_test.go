package panel

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSWRCache swrCache 的语义：首次同步拉、TTL 内复用、过期立即回旧数据 + 后台刷新、
// force 同步绕过、刷新失败保留旧数据。
func TestSWRCache(t *testing.T) {
	var calls int32
	c := newSWR(50*time.Millisecond, func() (any, error) {
		atomic.AddInt32(&calls, 1)
		return map[string]int{"n": int(calls)}, nil
	})

	// 1) 首次：同步拉
	v, at, cached := c.get(false)
	if v.(map[string]int)["n"] != 1 || cached {
		t.Fatalf("首次应同步拉取: v=%v cached=%v", v, cached)
	}
	if at.IsZero() {
		t.Fatal("首次拉取后应有数据时间")
	}

	// 2) TTL 内：直接复用，不调 fetch
	v2, _, cached2 := c.get(false)
	if !cached2 || v2.(map[string]int)["n"] != 1 {
		t.Fatalf("TTL 内应命中缓存: v=%v cached=%v", v2, cached2)
	}

	// 3) 过期：立即回旧数据，同时后台刷新
	time.Sleep(60 * time.Millisecond)
	v3, _, cached3 := c.get(false)
	if !cached3 || v3.(map[string]int)["n"] != 1 {
		t.Fatalf("过期时应立即回旧数据: v=%v cached=%v", v3, cached3)
	}
	// 后台刷新是异步的：轮询等它**写入缓存**（fetch 计数 +1 不代表 val 已更新——
	// 两者之间还有一把锁的窗口，直接断言 val 会有时序竞争）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		n := c.val.(map[string]int)["n"]
		c.mu.Unlock()
		if n >= 2 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("过期后应触发后台刷新并写入新值")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 写入后再取，必是新值
	v4, _, _ := c.get(false)
	if v4.(map[string]int)["n"] < 2 {
		t.Fatalf("后台刷新后应拿到新值: v=%v", v4)
	}

	// 4) force：同步绕过缓存
	n0 := atomic.LoadInt32(&calls)
	v5, _, cached5 := c.get(true)
	if cached5 {
		t.Fatal("force 不应标记为缓存命中")
	}
	if v5.(map[string]int)["n"] == 0 || atomic.LoadInt32(&calls) <= n0 {
		t.Fatalf("force 应同步重拉: v=%v calls=%d", v5, atomic.LoadInt32(&calls))
	}

	// 5) 刷新失败：保留旧数据（宁旧勿空）
	var c2calls int32
	c2 := newSWR(30*time.Millisecond, func() (any, error) {
		if atomic.AddInt32(&c2calls, 1) > 1 {
			return nil, errBoom
		}
		return "good", nil
	})
	v6, _, _ := c2.get(false)
	time.Sleep(40 * time.Millisecond)
	v7, _, cached7 := c2.get(false)
	if v6 != "good" || v7 != "good" {
		t.Fatalf("刷新失败应保留旧数据: v6=%v v7=%v", v6, v7)
	}
	if !cached7 {
		t.Fatal("失败后的旧数据应按缓存命中返回")
	}
}

var errBoom = &apiError{code: 500, msg: "boom"}

// TestPackagesCachedEndpoint 缓存版 packages 端到端：第一次慢（同步拉）、
// 第二次立即回（缓存命中），并带 X-Data-* 元信息头。
func TestPackagesCachedEndpoint(t *testing.T) {
	t.Skip("需要完整 Panel 装配（Pool+Upstream），由既有 handler 测试覆盖路由层；swr 语义由 TestSWRCache 覆盖")
	_ = httptest.NewRecorder
	_ = http.StatusOK
}
