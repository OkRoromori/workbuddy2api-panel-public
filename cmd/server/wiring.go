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
// 本分支的 realmAwareAvailableForModel 带 live 快照：**每次调用现读** model_realm
// 配置（逐模型 pin 优先 → 全局 prefer → 缺省 cn），面板改「重名模型指定域 / 默认域
// 优先级」后粘性绑号立刻按新域走，否则配置改了却仍绑旧域（表现为"配置不生效"）。
// realm 为空串时 pool.WeightedAvailableUIDsForModelRealm 退化为现状
// （AvailableUIDsForModel），老调用（无前缀模型名）语义零改动；返回列表可能对
// 快过期账号重复同一 UID（虚拟实例权重），会话哈希分配无需感知权重细节。
func realmAwareAvailableForModel(p *pool.Pool, live *livecfg.Holder) func(model string) []string {
	return func(model string) []string {
		snap := live.Load()
		realm, bare := server.ResolveModelWithRealm(model, snap.ModelRealmPrefer, snap.ModelRealmPins)
		return p.WeightedAvailableUIDsForModelRealm(bare, realm)
	}
}
