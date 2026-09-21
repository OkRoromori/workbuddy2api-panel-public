package panel

import (
	"net/http"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/httpauth"
)

// stats 仪表盘数据：进程内用量指标快照。
//
// 与 overview 的分工：overview 回答「池现在是什么状态」（谁可用、谁冷却、健康度），
// stats 回答「一共跑了多少」（累计 token / 请求 / 积分消耗速率）。数据源、
// 生命周期、刷新节奏都不同，所以是独立接口而不是塞进 overview。
//
// 账号维度只回原始 uid，展示名由前端用 overviewData 里的别名表映射——
// 别名是纯展示层数据，不该让后端为它多查一次。
func (p *Panel) stats(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Metrics == nil {
		writeErr(w, http.StatusNotImplemented, "指标未启用：服务启动时未注入 metrics.Registry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"stats": p.cfg.Metrics.Snapshot(),
		// 密钥指纹 → 显示名。只给**指纹**与名字，不给密钥本体：
		// 仪表盘只需要把指纹翻译成人能读的标签，没有任何理由把可用密钥
		// 塞进这个每 5 秒轮询一次的接口。
		"key_names": p.keyNames(),
	})
}

// keyNames 构造 指纹 → 显示名 的映射。
//
// 名字来自运行期快照（Live），而不是每次 Load 配置文件：
// stats 是自动轮询接口（前端 5 秒一次），走磁盘会让「面板每刷新一下就
// 读一次 config.json」。快照本身就是热更新后的权威值（保存密钥会 Store 新快照），
// 口径与网关鉴权完全一致。
//
// 未启用鉴权（无密钥表）时返回空 map，前端会回落到显示「未鉴权」。
func (p *Panel) keyNames() map[string]string {
	if p.cfg.Live == nil {
		return map[string]string{}
	}
	keys := p.cfg.Live.Load().ClientKeys()
	out := make(map[string]string, len(keys))
	for k, name := range keys {
		// 与密钥页/审计页同一个回落口径，否则同一把密钥在两个页面显示
		// 不同的名字，用量对不上时会让人以为统计错了。
		out[httpauth.Fingerprint(k)] = displayKeyName(name)
	}
	return out
}
