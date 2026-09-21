// extra_handlers.go 本地功能线独有的面板 handler（v1.11 基座上补齐）。
//
// 这些端点 v1.11 没有，但本地部署在用：
//
//	accountAlias    账号显示别名（alias.go 的存储层，本文件是 HTTP 入口）
//	restart         请求进程退出以便启动器重编译拉起
//	probeAll        手动触发全量验活
//	deadAudit       手动触发死号自检
//	reviveAll       批量解冻全部禁用账号
//	removeDisabled  批量移除禁用账号（破坏性）
//	autoAllPool     全池一键完成任务
//	taskClaimAll    领取该账号全部可领奖励
package panel

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// RestartExitCode 重启约定的退出码：启动器（start-windows.ps1 / systemd）
// 看到该退出码后重编译并拉起。
const RestartExitCode = 75

// accountAlias 设置/清除账号显示别名。
//
// 别名是面板本地的展示覆盖层，不写进 auths/ 凭据（那会被 token 刷新重写抹掉，
// 见 alias.go 说明），也不影响选号与上游账号本身。
// body: {"alias":"..."}；alias 传空串 = 清除别名，回落原始昵称。
func (p *Panel) accountAlias(w http.ResponseWriter, r *http.Request) {
	if p.cfg.AliasPath == "" {
		writeErr(w, http.StatusNotImplemented, "alias api not available")
		return
	}
	uid := r.PathValue("uid")
	if _, ok := p.cfg.Pool.Status(uid); !ok {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var body struct {
		Alias string `json:"alias"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	alias, verr := normalizeAlias(body.Alias)
	if verr != "" {
		writeErr(w, http.StatusBadRequest, verr)
		return
	}
	if p.aliases == nil {
		p.aliases = newAliasStore(p.cfg.AliasPath)
	}
	if err := p.aliases.set(uid, alias); err != nil {
		writeErr(w, http.StatusInternalServerError, "save alias: "+err.Error())
		return
	}
	if alias == "" {
		log.Printf("panel: 已清除别名 uid=%s", uid)
	} else {
		log.Printf("panel: 已设置别名 uid=%s -> %s", uid, alias)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uid": uid, "alias": alias})
}

// restart 请求进程退出。启动器看到退出码 RestartExitCode 后会重编（若源码更新）再拉起。
// 响应先写出再异步触发，避免连接被掐在 JSON 写完之前。
func (p *Panel) restart(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Restart == nil {
		writeErr(w, http.StatusNotImplemented, "restart not available")
		return
	}
	log.Printf("panel: 收到重启请求")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		p.cfg.Restart()
	}()
}

// probeAll 手动触发一轮账号验活（异步执行，结果看账号页「验活」列与日志区）。
//
// 与 balanceAll 的区别：余额刷新是同步等待的（账号数少、要立刻看到新余额），
// 验活每个账号要真跑一次推理、还带账号间隔，同步等会让面板转很久，
// 所以这里异步触发、让用户看状态列自己更新。
func (p *Panel) probeAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	go p.cfg.Scheduler.RunProbeNow()
	log.Printf("panel: 手动账号验活已触发（每个可用账号发一次极小请求）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// deadAudit 手动触发死号自检（异步执行，结果看日志区）。
//
// 保活排程（keepalive_hours，默认 22 点）会自动跑一次；这里是「不想等到晚上」
// 的手动入口——刚发现有号变灰、或刚修完 12153 相关问题时立刻验证。
// 自检只复活「refresh 成功」的号，不会把真死号放回来。
func (p *Panel) deadAudit(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	go p.cfg.Scheduler.RunDeadAuditNow()
	log.Printf("panel: 手动死号自检已触发（refresh 成功的误判号会自动复活）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// reviveAll 批量解冻所有禁用账号（运维口径：清禁用 + 冷却 + 熔断 + 12153 计数）。
//
// 与死号自检（deadAudit）的区别：自检只复活「refresh 成功」的号，本接口无条件复活
// 全部 disabled —— 用于「确认这些号其实没问题、只想让它们立刻回到池子」的场景。
// 返回实际复活数量，前端据此提示。
func (p *Panel) reviveAll(w http.ResponseWriter, r *http.Request) {
	uids := p.disabledUIDs()
	for _, uid := range uids {
		p.cfg.Pool.Revive(uid)
	}
	log.Printf("panel: 批量解冻完成，复活 %d 个账号", len(uids))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revived": len(uids)})
}

// removeDisabled 批量移除所有禁用账号（含删除 auths/ 下的凭证文件）。
//
// ⚠️ 破坏性且不可逆：凭证文件删除后该账号只能重新登录。
// 前端必须先弹二次确认。只想"试试能不能救回来"的应该先用死号自检 / 批量解冻，
// 确认救不回来再走这里。
//
// 部分失败不影响其余账号；凭证文件删不掉的账号保留在池中，并逐个汇报 file_error。
func (p *Panel) removeDisabled(w http.ResponseWriter, r *http.Request) {
	uids := p.disabledUIDs()
	if len(uids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": 0})
		return
	}
	removed, failed := 0, 0
	var errs []string
	for _, uid := range uids {
		a := p.cfg.Pool.AuthByUID(uid)
		if a == nil {
			continue
		}
		if a.FilePath != "" {
			if err := os.Remove(a.FilePath); err != nil && !os.IsNotExist(err) {
				failed++
				errs = append(errs, uid+": "+err.Error())
				continue
			}
		}
		p.cfg.Pool.Remove(uid)
		removed++
	}
	log.Printf("panel: 批量移除禁用账号，移除 %d 失败 %d", removed, failed)
	resp := map[string]any{"ok": true, "removed": removed, "failed": failed}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	writeJSON(w, http.StatusOK, resp)
}

// disabledUIDs 返回当前全部禁用账号的 uid。
func (p *Panel) disabledUIDs() []string {
	var out []string
	for _, st := range p.cfg.Pool.List() {
		if st.Disabled {
			out = append(out, st.UID)
		}
	}
	return out
}

// autoAllPool 全池一键完成任务（异步执行）。
func (p *Panel) autoAllPool(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Pool == nil {
		writeErr(w, http.StatusNotImplemented, "pool not available")
		return
	}
	go p.runAutoAllPool()
	log.Printf("panel: 全部做任务已触发")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// runAutoAllPool 全池逐号执行一键完成任务（跳过禁用/无凭证/已在跑的号）。
func (p *Panel) runAutoAllPool() {
	start := time.Now()
	list := p.cfg.Pool.List()
	total, ran, skipped := 0, 0, 0
	for _, st := range list {
		total++
		if st.Disabled {
			skipped++
			continue
		}
		a := p.cfg.Pool.AuthByUID(st.UID)
		if a == nil || !a.HasAccessToken() {
			skipped++
			continue
		}
		if !p.tryLockAccount(st.UID) {
			log.Printf("panel: 全部做任务 uid=%s 跳过（该号已有任务在跑）", st.UID)
			skipped++
			continue
		}
		func() {
			defer p.unlockAccount(st.UID)
			p.runAutoAll(a)
		}()
		ran++
	}
	log.Printf("panel: 全部做任务 全部结束 ✅ 账号 %d（执行 %d / 跳过 %d）耗时 %s",
		total, ran, skipped, time.Since(start).Round(time.Second))
}

// taskClaimAll 领取该账号全部可领奖励（进度已达标、尚未 claimed）。
func (p *Panel) taskClaimAll(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	tasks, err := p.cfg.Upstream.ListTasks(a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list tasks: "+err.Error())
		return
	}
	var claimed int
	var credit, energy int64
	var failed []string
	for _, t := range tasks {
		if t.Claimed || !t.Claimable {
			continue
		}
		c, e, err := p.cfg.Upstream.ClaimReward(a, t.TaskCode)
		if err != nil {
			log.Printf("panel: 批量领取失败 uid=%s code=%s err=%v", uid, t.TaskCode, err)
			failed = append(failed, t.TaskCode)
			continue
		}
		claimed++
		credit += c
		energy += e
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("panel: 全部领取 uid=%s 领取=%d 失败=%d +%d分 +%d能", uid, claimed, len(failed), credit, energy)
	resp := map[string]any{"ok": true, "claimed": claimed, "credit": credit, "energy": energy, "failed": failed}
	if claimed == 0 && len(failed) == 0 {
		resp["message"] = "没有可领取的奖励"
	}
	writeJSON(w, http.StatusOK, resp)
}
