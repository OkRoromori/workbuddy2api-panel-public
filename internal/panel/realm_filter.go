// realm_filter.go 审计流水的「按域过滤」共用实现。
//
// 为什么抽出来：审计明细（usage_day.go）、近 N 天趋势（trend.go）、密钥用量（keys.go）三处都要
// 把流水行映射回账号所属域，各写一遍迟早出现口径差异——而这三处在面板上显示的是同一批请求的
// 不同切面，"总和对得上、明细对不上"是最难查的一类问题。
//
// 一条规则贯穿三处：**号已从池中移除的历史行在域模式下不计入任何域**（它已不属于任何现役域），
// 「全部」模式下照常显示（历史不丢）。
package panel

import "github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"

// realmOfAccounts 账号 uid 前 8 位 → 域（cn/global）。审计行里只存 uid8，故按 uid8 索引；
// 同一个 uid8 在现役池里只会对应一个账号（uid 前 8 位冲突的概率可忽略，且真冲突也只是归错域，
// 不会崩）。
func (p *Panel) realmOfAccounts() map[string]string {
	list := p.cfg.Pool.List()
	out := make(map[string]string, len(list))
	for _, s := range list {
		out[shortUID(s.UID)] = s.Realm
	}
	return out
}

// filterByRealm 按域过滤审计记录。realm 非 cn/global 时原样返回（「全部」= 不过滤）。
func filterByRealm(recs []audit.Record, realmOf map[string]string, realm string) []audit.Record {
	if realm != "cn" && realm != "global" {
		return recs
	}
	kept := make([]audit.Record, 0, len(recs))
	for _, rec := range recs {
		if realmOf[rec.Account] == realm {
			kept = append(kept, rec)
		}
	}
	return kept
}

// normalizeRealm 归一查询参数里的 realm：只认 cn/global，其余（含空）返回 ""（= 全部）。
func normalizeRealm(raw string) string {
	switch raw {
	case "cn", "global":
		return raw
	default:
		return ""
	}
}
