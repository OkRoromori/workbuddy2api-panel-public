package main

import (
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/livecfg"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/pool"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/server"
)

// realmAwareAvailableForModel 构造会话粘性路由按模型可用口径的 realm 感知闭包。
//
// 粘性分配的模型名可能带 realm 前缀（"global:gpt-5.4" / "cn:glm-5.2"）：必须剥出
// realm + bareModel，再交给分池选号域过滤——否则裸名取池子全集，global 号会被粘性分配给
// CN 前缀请求（跨 realm 泄漏）。
//
// 裸名（常态：/v1/models 下发的一律是裸名）的域归属读 livecfg 快照的 model_realm 配置：
// 逐模型 pin 优先 → 全局 prefer → 缺省 cn（去前缀前的历史语义）。**每次调用现读快照**，
// 不在构造时固化：面板改「重名模型指定域 / 默认域优先级」后，粘性绑号必须立刻按新域走，
// 否则配置改了却仍绑在旧域的账号上（表现为"配置不生效"）。
func realmAwareAvailableForModel(p *pool.Pool, live *livecfg.Holder) func(model string) []string {
	return func(model string) []string {
		snap := live.Load()
		realm, bare := server.ResolveModelWithRealm(model, snap.ModelRealmPrefer, snap.ModelRealmPins)
		return p.AvailableUIDsForModelRealm(bare, realm)
	}
}
