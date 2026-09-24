package panel

import (
	"sync"
	"time"
)

// swrCache 面板慢接口的结果缓存（stale-while-revalidate）。
//
// 面板有三个接口的耗时来自"打上游"而非本机计算：
//   - /panel/api/packages  逐账号向上游实时查询（23 号 × 并发 3 ≈ 4s）
//   - /panel/api/usage     全量读当天审计 JSONL 再聚合（≈1.2s）
//   - /panel/api/models    并发探两路上游模型目录（≈0.7s）
//
// 这三个接口的数据变化频率都远低于访问频率（余额 5 分钟刷一次、审计一条条落盘、
// 模型目录上游自己缓存 1 小时），每次点开面板都真打一遍上游是纯浪费——而且
// packages 那种 4 秒的等待，用户感知就是"面板卡"。
//
// 语义（stale-while-revalidate）：
//   - 有未过期数据 → 立即返回（微秒级）
//   - 数据过期但存在 → **先立即返回旧数据**，同时后台异步刷新（下次就是新的）
//   - 从未拉过 → 同步拉一次（此时只能等，但只发生在首次/重启后）
//
// 并发安全：多个请求同时命中"过期"时，只有一个去刷（singleflight 语义，
// 用 mutex + 标志位实现，不引外部依赖）。
type swrCache struct {
	mu         sync.Mutex
	val        any
	fetched    time.Time
	ttl        time.Duration
	refreshing bool
	fetch      func() (any, error)
}

func newSWR(ttl time.Duration, fetch func() (any, error)) *swrCache {
	return &swrCache{ttl: ttl, fetch: fetch}
}

// get 返回缓存的数据；过期则后台刷新。force=true 时同步强制刷新
// （面板上的「重新查询」按钮用，绕过缓存）。
//
// 返回 (数据, 数据时间, 是否命中缓存)。从未拉到过数据时 data 为 nil。
func (c *swrCache) get(force bool) (any, time.Time, bool) {
	c.mu.Lock()
	if force || c.val == nil {
		// 首次或强制：同步拉（持锁拉取——首次只有一个请求，后来者等同一个结果，
		// 比各自打一遍上游好）。
		v, err := c.fetch()
		if err == nil {
			c.val, c.fetched = v, time.Now()
		} else if c.val != nil {
			// 刷新失败：保留旧数据（宁旧勿空），本次按旧数据返回。
			// 不改 fetched，让下一次请求再试。
		} else {
			c.mu.Unlock()
			return nil, time.Time{}, false
		}
		out, at := c.val, c.fetched
		c.mu.Unlock()
		return out, at, false
	}
	stale := time.Since(c.fetched) >= c.ttl
	out, at := c.val, c.fetched
	if stale && !c.refreshing {
		c.refreshing = true
		go func() {
			v, err := c.fetch()
			c.mu.Lock()
			if err == nil {
				c.val, c.fetched = v, time.Now()
			}
			c.refreshing = false
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	return out, at, true
}

// age 数据年龄（供前端显示"数据时间"）。从未拉过返回 0 值时间。
func (c *swrCache) age() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetched
}
