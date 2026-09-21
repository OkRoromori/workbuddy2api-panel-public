package server

import "strings"

// 模型名协议与域解析。
//
// 入参有两条形态：
//
//	显式前缀  "[realm:]model"  realm ∈ {cn, global} —— 客户端强制指定域
//	裸名      "model"                               —— 常态：域由配置决定
//
// 2026-09-20 变更（用户反馈）：/v1/models 一律下发**裸名**。此前给 CN 模型加 "cn:"、
// global 加 "global:"，导致每个客户端拉完模型列表都要手工删前缀才能用；而域只是网关的
// 路由细节，不该出现在调用值里（面板用徽标展示归属即可）。前缀退化为**可选**的显式指定
// 手段：想强制走某域时仍可写 "global:gpt-5.6-luna"。
//
// 裸名的域归属（重名模型两域都有时按此取舍）：
//
//	1. model_realm.pins[模型名]  逐模型钉死（面板模型页「重复模型」里逐个钉）
//	2. model_realm.prefer        全局默认（面板模型页「默认域优先级」，缺省 cn）
//
// 缺省 cn 是刻意的：去前缀之前裸名就按 cn 解析，缺省值保持历史语义，老客户端零回归。
//
// ⚠ 但配置只是**偏好**：目录事实优先（见 resolveModelRealm）——只有单一域有的模型必须
// 强制走那一域。此前只用 pin/prefer，导致裸名 gpt-6-astra（仅 global 有）被送去国服，
// 上游回 11102 service info not found。
func resolveModelWithRealm(model, prefer string, pins map[string]string) (realm, bare string) {
	realm, bare, _ = resolveModelRealm(model, prefer, pins, ModelRealmCatalog{})
	return realm, bare
}

// ModelRealmCatalog 请求侧「域校准」所需的目录视图。
//
// 两侧各带一个"已知"标志：**只有缺席的那一侧已知**，才能断言"只有另一个域有它"——
// 例如 CN 目录已缓存且不含 gpt-6-astra，同时 global 目录已知且含它，才能强制走 global。
// 任一侧未知就退回配置解析（宁可沿用偏好，也不猜）。
// 关键：两侧都只读缓存，**绝不为了解析域触发探测**（聊天请求不能被目录拉取阻塞）。
type ModelRealmCatalog struct {
	CN          []string
	Global      []string
	CNKnown     bool
	GlobalKnown bool
}

// resolveModelRealm 带目录校准的域解析。soft=true 表示"这个域是按偏好/默认选出来的"——
// 调用方可在该域**没有可用账号**时回落到另一域（见 otherRealm）；硬指定（显式前缀 / 逐模型
// 钉死 / 目录证明只有一域有）不回退：宁可直接报错，也不悄悄换域（换域意味着不同的额度口径）。
//
//	1. 显式前缀 "global:x" → 硬（客户端明确要的就是这个域）
//	2. 裸名且目录证明只有一个域有它 → 硬（事实优先于偏好）
//	3. 裸名两域都有（或一侧目录未知）→ pins 命中 → 硬；否则 prefer → 软
func resolveModelRealm(model, prefer string, pins map[string]string, cat ModelRealmCatalog) (realm, bare string, soft bool) {
	if r, b, ok := parseModelPrefix(model); ok {
		return r, b, false
	}
	cnHas, glHas := catalogHas(cat, model)
	if glHas && cat.CNKnown && !cnHas {
		return "global", model, false // 只有国际服有
	}
	if cnHas && cat.GlobalKnown && !glHas {
		return "cn", model, false // 只有国服有
	}
	if r := pins[model]; r == "cn" || r == "global" {
		return r, model, false
	}
	if prefer == "global" {
		return "global", model, true
	}
	return "cn", model, true
}

// catalogHas 该裸名在目录两侧的可用性。目录未知（cat 零值）时两者皆为 false。
func catalogHas(cat ModelRealmCatalog, bare string) (cn, global bool) {
	for _, id := range cat.CN {
		if id == bare {
			cn = true
			break
		}
	}
	for _, id := range cat.Global {
		if id == bare {
			global = true
			break
		}
	}
	return cn, global
}

// otherRealm 另一个域（软解析的回落目标）。
func otherRealm(realm string) string {
	if realm == "global" {
		return "cn"
	}
	return "global"
}

// parseModelPrefix 剥显式域前缀。取第一个 ":"，前段恰为 cn/global 才认可（大小写敏感，
// 与历史一致）；其余一律按裸名处理——含 "foo:bar" 这类名字里带冒号的模型。
func parseModelPrefix(model string) (realm, bare string, explicit bool) {
	idx := strings.IndexByte(model, ':')
	if idx <= 0 {
		return "", model, false
	}
	switch prefix := model[:idx]; prefix {
	case "cn", "global":
		return prefix, model[idx+1:], true
	default:
		return "", model, false
	}
}

// resolveModel 无配置上下文的历史签名：等价 prefer=cn、无 pins。
func resolveModel(model string) (realm, bare string) {
	return resolveModelWithRealm(model, "cn", nil)
}

// ResolveModel 是 resolveModel 的导出面（跨包调用，历史签名）。
func ResolveModel(model string) (realm, bare string) { return resolveModel(model) }

// ResolveModelWithRealm 导出面：跨包（cmd/server 的粘性闭包）需要带配置的完整解析。
func ResolveModelWithRealm(model, prefer string, pins map[string]string) (realm, bare string) {
	return resolveModelWithRealm(model, prefer, pins)
}
