// Package livecfg 运行期可变配置的并发安全持有者。
//
// 背景：进程启动时读入的配置是普通字段（读多写零），但管理面板允许在线改配置，
// 于是少量"可热生效"的字段需要有并发安全的读写点。此处用不可变快照 + atomic 指针：
// 读方 Load 拿到一致视图，写方 Store 整体替换，无锁无数据竞争。
//
// 只承载**读路径深、热改需求强**的少数字段；池参数/排程参数等各有既有 setter
// （pool.SetBreaker、scheduler.Reconfigure 等），不重复收编到这里。
package livecfg

import (
	"sync/atomic"
	"time"
)

// KeyPolicy 单把客户端密钥的调用限制。Models 为空表示允许全部模型，
// MaxConcurrency 为 0 表示不单独限制并发。
type KeyPolicy struct {
	Models         []string `json:"models,omitempty"`
	MaxConcurrency int      `json:"max_concurrency,omitempty"`
}

// Snapshot 一次读取的不可变配置视图。
type Snapshot struct {
	APIKey               string        // 网关/面板共同鉴权密钥；空 = 不鉴权
	SoftCooldown         time.Duration // 429 软冷却基数（<=0 时调用方回退内置默认）
	SanitizeFingerprints bool          // 出站请求体指纹脱敏

	// Keys 网关 /v1 的多密钥表（密钥 → 审计名），含主密钥。
	//
	// 不可变约定：Store 之后任何人不许再改这个 map（读方拿到的是同一份引用，
	// 就地修改会造成数据竞争）。每次热更新都整体构造一张新表。
	Keys map[string]string

	// KeyPolicies 按密钥指纹索引。与 Keys 一样遵守 Store 后不可修改的约定。
	KeyPolicies map[string]KeyPolicy

	// ModelRealmPrefer 裸模型名的默认域："cn"（缺省）| "global"。只影响重名模型
	//（两域都有同名）的取舍；客户端显式写 "global:x" 时前缀依然优先。
	ModelRealmPrefer string

	// ModelRealmPins 逐模型钉死域（裸模型名 → "cn"|"global"），优先于 ModelRealmPrefer。
	// 与 Keys 一样遵守 Store 后不可修改的约定。
	ModelRealmPins map[string]string
}

// ClientKeys 返回网关鉴权用的密钥表。
//
// 兜底：快照可能只设了 APIKey（旧构造路径/测试），此时补一张单条目表，
// 否则「配了密钥」却因为 Keys 为空而被当成"未启用鉴权"直接放行——方向性的安全漏洞。
func (s Snapshot) ClientKeys() map[string]string {
	if len(s.Keys) > 0 {
		return s.Keys
	}
	if s.APIKey == "" {
		return nil
	}
	return map[string]string{s.APIKey: ""}
}

// Holder 原子持有当前快照。
type Holder struct {
	p atomic.Pointer[Snapshot]
}

// New 以初始快照构建。
func New(s Snapshot) *Holder {
	h := &Holder{}
	h.Store(s)
	return h
}

// Load 返回当前快照（Holder 为 nil 或从未 Store 时返回零值快照，调用方无需判空）。
func (h *Holder) Load() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	if s := h.p.Load(); s != nil {
		return *s
	}
	return Snapshot{}
}

// Store 整体替换快照。
func (h *Holder) Store(s Snapshot) { h.p.Store(&s) }
