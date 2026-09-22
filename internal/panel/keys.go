// keys.go 面板「密钥」页：额外 API 密钥的创建 / 改名 / 删除。
//
// 与「设置」页的分工（用户明确要求的口径）：
//   - 默认密钥（config.api_key）留在设置页——它同时是**面板登录密码**，
//     和一堆服务级配置放在一起才好找，也不该被"随手删一个密钥"误伤。
//   - 其余密钥（config.api_keys）全部在这里管理：建、改名、删。
//
// 为什么要有这一页：多密钥之前是让用户在一行 textarea 里手写「密钥 名字」，
// 密钥要自己编（容易编出弱密钥）、删错了看不出来、也不知道哪个密钥还有人在用。
// 这里改成按钮流：服务端用 crypto/rand 生成、列表一眼看清、删除前给二次确认。
package panel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/httpauth"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/livecfg"
)

// errNotAvailable 面板未注入该能力（501 响应已写好，调用方直接 return）。
var errNotAvailable = errors.New("panel: capability not available")

// KeysView 密钥页需要的配置视图（由 main 注入的闭包提供）。
type KeysView struct {
	Owner     string                       `json:"owner"`              // 默认密钥（兼面板登录密码）；空 = 未启用鉴权
	OwnerName string                       `json:"owner_name"`         // 默认密钥的显示名
	Extra     map[string]string            `json:"extra"`              // 额外密钥（密钥 → 显示名）
	Policies  map[string]livecfg.KeyPolicy `json:"policies,omitempty"` // 密钥指纹 → 调用限制
}

// keyStatDays 密钥用量回看的窗口天数（与审计保留期一致）。
const keyStatDays = 7

// maxKeyNameLen 密钥显示名长度上限（按字符）。与 cmd/server 的清洗口径一致，
// 这里再挡一道是为了让前端立刻拿到明确报错，而不是等保存时才失败。
const maxKeyNameLen = 24

// keyEntry 密钥页的一行。
//
// Key 返回**完整密钥**：这一页的核心用途之一就是把密钥复制给对方，
// 只给掩码等于让人没法用。面板本身已要求默认密钥鉴权（而默认密钥就是登录密码），
// 所以这不是新增暴露面——设置页本来也回显同一串。
type keyEntry struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Key            string   `json:"key"`
	Masked         string   `json:"masked"`
	Owner          bool     `json:"owner"` // true = 默认密钥（兼面板密码），不可删
	Requests       int      `json:"requests"`
	Failed         int      `json:"failed"`
	Credit         float64  `json:"credit"`
	Models         []string `json:"models,omitempty"`
	MaxConcurrency int      `json:"max_concurrency,omitempty"`
}

// keyID 密钥的稳定短标识。
//
// 直接复用 httpauth.Fingerprint，**不另写一份**：这个值要和审计流水里的 key_id
// 逐字节相等，两处各写一份哈希迟早会漂移（改了长度、换了算法），
// 而症状是"密钥页永远显示没人用过"——很难联想到这里。
//
// 用它当 URL 里的 id 而不是把密钥本身塞进路径：路径会进浏览器历史、
// 代理日志、Referer，把一把还能用的密钥留在那些地方是不必要的暴露。
func keyID(key string) string { return httpauth.Fingerprint(key) }

// maskKey 密钥掩码：留头 6 位 + 尾 4 位，中间省略。
// 短密钥（理论上不该出现）整体打点，避免"掩码比原文还长"这种滑稽输出。
func maskKey(key string) string {
	r := []rune(key)
	if len(r) <= 12 {
		return strings.Repeat("•", len(r))
	}
	return string(r[:6]) + "……" + string(r[len(r)-4:])
}

// genAPIKey 生成一把新密钥：crypto/rand 24 字节 → base64url，前缀 sk-。
// 用 base64url 是为了让它能安全地出现在配置/URL/剪贴板里而无需转义。
func genAPIKey() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "sk-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// normalizeKeyName 清洗显示名：去空白 + 按字符截断。
func normalizeKeyName(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > maxKeyNameLen {
		return string(r[:maxKeyNameLen])
	}
	return s
}

// keys 列出全部密钥：默认密钥恒排第一，其余按用量降序（用得多的在上面），
// 用量相同按名字，保证每次渲染顺序稳定——否则刷新一下行序就跳。
func (p *Panel) keys(w http.ResponseWriter, r *http.Request) {
	v, err := p.loadKeys(w)
	if err != nil {
		return
	}
	// 密钥列表的用量列跟随域开关（密钥本身与域无关，但"这把密钥在这个域花了多少"是有意义的）
	realm := normalizeRealm(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("realm"))))
	stats := p.auditKeyStats(keyStatDays, realm)

	var owner *keyEntry
	if v.Owner != "" {
		e := keyEntry{
			ID: keyID(v.Owner), Name: displayKeyName(v.OwnerName),
			Key: v.Owner, Masked: maskKey(v.Owner), Owner: true,
		}
		applyStat(&e, stats)
		owner = &e
	}
	// 额外密钥单独装一份再排序：不能借用 out 的底层数组（append 会就地覆写，
	// 而排序切片和它共享内存，结果就是整张表串行）。
	rest := make([]keyEntry, 0, len(v.Extra))
	for k, name := range v.Extra {
		e := keyEntry{ID: keyID(k), Name: displayKeyName(name), Key: k, Masked: maskKey(k)}
		if policy, ok := v.Policies[e.ID]; ok {
			e.Models = append([]string(nil), policy.Models...)
			e.MaxConcurrency = policy.MaxConcurrency
		}
		applyStat(&e, stats)
		rest = append(rest, e)
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Requests != rest[j].Requests {
			return rest[i].Requests > rest[j].Requests
		}
		return rest[i].Name < rest[j].Name
	})

	out := make([]keyEntry, 0, len(rest)+1)
	if owner != nil {
		out = append(out, *owner)
	}
	out = append(out, rest...)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"keys":      out,
		"stat_days": keyStatDays,
		"max_name":  maxKeyNameLen,
	})
}

// keyCreate 新建一把密钥。名字由用户给，密钥本体由服务端生成。
//
// 服务端生成而不是让用户自己编：手编的密钥通常是 `sk-123456` 这种，
// 而这里生成的是 24 字节随机。
func (p *Panel) keyCreate(w http.ResponseWriter, r *http.Request) {
	v, err := p.loadKeys(w)
	if err != nil {
		return
	}
	name, ok := p.readKeyName(w, r)
	if !ok {
		return
	}
	key, gerr := genAPIKey()
	if gerr != nil {
		writeErr(w, http.StatusInternalServerError, "生成密钥失败："+gerr.Error())
		return
	}
	extra := cloneKeys(v.Extra)
	extra[key] = name
	// 条数上限由配置层校验（normalize），这里不重复实现一份阈值——
	// 两处各写一个上限，早晚会漂移成"面板能建但保存失败"。
	if err := p.saveKeySettings(w, extra, clonePolicies(v.Policies)); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"key": keyEntry{
			ID: keyID(key), Name: displayKeyName(name),
			Key: key, Masked: maskKey(key),
		},
	})
}

// keyRename 改显示名。
//
// 注意：审计流水里的 user 是**写入当时的名字快照**，改名不会追溯修改历史行。
// 前端会提示这一点，否则用户会以为"改了名但统计没变"是 bug。
func (p *Panel) keyRename(w http.ResponseWriter, r *http.Request) {
	v, err := p.loadKeys(w)
	if err != nil {
		return
	}
	id := r.PathValue("id")
	settings, ok := p.readKeySettings(w, r)
	if !ok {
		return
	}
	extra := cloneKeys(v.Extra)
	found := false
	for k := range extra {
		if keyID(k) == id {
			extra[k] = settings.Name
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "找不到这把密钥（可能已被删除）")
		return
	}
	policies := clonePolicies(v.Policies)
	if len(settings.Models) == 0 && settings.MaxConcurrency == 0 {
		delete(policies, id)
	} else {
		policies[id] = livecfg.KeyPolicy{Models: settings.Models, MaxConcurrency: settings.MaxConcurrency}
	}
	if err := p.saveKeySettings(w, extra, policies); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// keyDelete 删除一把额外密钥。
//
// 默认密钥删不得：它同时是面板登录密码，删掉等于把自己锁在门外。
// 前端也不会给这个按钮，这里是服务端兜底（前端可以被绕过）。
func (p *Panel) keyDelete(w http.ResponseWriter, r *http.Request) {
	v, err := p.loadKeys(w)
	if err != nil {
		return
	}
	id := r.PathValue("id")
	if v.Owner != "" && keyID(v.Owner) == id {
		writeErr(w, http.StatusBadRequest, "默认密钥不能删：它同时是面板登录密码。要换请在「设置」里改。")
		return
	}
	extra := cloneKeys(v.Extra)
	found := false
	for k := range extra {
		if keyID(k) == id {
			delete(extra, k)
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "找不到这把密钥（可能已被删除）")
		return
	}
	policies := clonePolicies(v.Policies)
	delete(policies, id)
	if err := p.saveKeySettings(w, extra, policies); err != nil {
		return
	}
	if err := p.revokeTunnelKey(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "密钥已删除，但撤销连接脚本的隧道失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// loadKeys 取当前密钥视图；未注入时写下 501 并返回 error。
func (p *Panel) loadKeys(w http.ResponseWriter) (KeysView, error) {
	if p.cfg.LoadKeys == nil {
		writeErr(w, http.StatusNotImplemented, "密钥接口不可用：服务启动时未注入 LoadKeys")
		return KeysView{}, errNotAvailable
	}
	v, err := p.cfg.LoadKeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取配置失败："+err.Error())
		return KeysView{}, err
	}
	return v, nil
}

// saveKeys 落盘额外密钥表；未注入时写下 501 并返回 error。
func (p *Panel) saveKeys(w http.ResponseWriter, extra map[string]string) error {
	return p.saveKeySettings(w, extra, nil)
}

func (p *Panel) saveKeySettings(w http.ResponseWriter, extra map[string]string, policies map[string]livecfg.KeyPolicy) error {
	if p.cfg.SaveKeySettings != nil {
		if err := p.cfg.SaveKeySettings(extra, policies); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return err
		}
		return nil
	}
	if p.cfg.SaveKeys == nil {
		writeErr(w, http.StatusNotImplemented, "密钥接口不可用：服务启动时未注入 SaveKeys")
		return errNotAvailable
	}
	if err := p.cfg.SaveKeys(extra); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return err
	}
	return nil
}

type keySettings struct {
	Name           string   `json:"name"`
	Models         []string `json:"models"`
	MaxConcurrency int      `json:"max_concurrency"`
}

func (p *Panel) readKeySettings(w http.ResponseWriter, r *http.Request) (keySettings, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return keySettings{}, false
	}
	var body keySettings
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return keySettings{}, false
	}
	body.Name = normalizeKeyName(body.Name)
	if body.MaxConcurrency < 0 || body.MaxConcurrency > 100 {
		writeErr(w, http.StatusBadRequest, "max_concurrency must be between 0 and 100")
		return keySettings{}, false
	}
	seen := map[string]bool{}
	out := body.Models[:0]
	for _, model := range body.Models {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			out = append(out, model)
		}
	}
	body.Models = out
	return body, true
}

// readKeyName 解析 body 里的 name 字段。名字允许为空（配置层回落「默认」），
// 但长度会被截断，避免一个名字把表格撑变形。
func (p *Panel) readKeyName(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return "", false
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return "", false
	}
	return normalizeKeyName(body.Name), true
}

// keyStats 密钥用量的两个索引。
type keyStats struct {
	byKey  map[string]audit.Bucket // 按密钥指纹（新流水）
	byName map[string]audit.Bucket // 按显示名（指纹上线前的老流水）
}

// applyStat 把用量填进一行。
//
// 优先按指纹取（改名不丢），再叠加按名字取的老流水。每条记录只会进其中一个索引
// （见 auditKeyStats），所以相加不会重复计数。
func applyStat(e *keyEntry, stats keyStats) {
	var b audit.Bucket
	if v, ok := stats.byKey[e.ID]; ok {
		b = v
	}
	if v, ok := stats.byName[e.Name]; ok {
		b = audit.MergeBuckets(b, v)
	}
	e.Requests, e.Failed, e.Credit = b.Requests, b.Failed, b.Credit
}

// auditKeyStats 汇总最近 limit 天的用量，按**密钥指纹**归并。
//
// 为什么不按名字：名字是可以改的，User 字段存的是写入当时的快照。按名字统计时，
// 用户改个名，这一页的用量就归零，于是"没人用过 → 可以删"这个判断直接反向——
// 而删掉一把别人正在用的密钥是这一页唯一会伤到人的操作。
// 指纹不随改名变化，所以历史用量能一直跟着这把密钥。
//
// 老流水（指纹字段上线之前写的）没有 key_id，只能按名字归入 byName，
// 由 applyStat 叠加。它们的名字如果后来被改过就对不上了——这是无法追溯的，
// 但至少不会把新流水的统计弄错。
func (p *Panel) auditKeyStats(limit int, realm string) keyStats {
	out := keyStats{byKey: map[string]audit.Bucket{}, byName: map[string]audit.Bucket{}}
	if p.cfg.Audit == nil {
		return out
	}
	days := p.cfg.Audit.Days()
	if len(days) > limit {
		days = days[:limit]
	}
	// 域过滤（面板顶部的国服/国际服开关）：与请求审计页/趋势图共用同一实现，
	// 三处显示的是同一批请求的不同切面，口径必须一致。
	var realmOf map[string]string
	if realm == "cn" || realm == "global" {
		realmOf = p.realmOfAccounts()
	}
	for _, d := range days {
		res, err := p.cfg.Audit.ReadDay(d)
		if err != nil {
			continue
		}
		recs := filterByRealm(res.Records, realmOf, realm)
		for k, b := range audit.GroupBy(recs, func(r audit.Record) string { return r.KeyID }) {
			out.byKey[k] = audit.MergeBuckets(out.byKey[k], b)
		}
		for k, b := range audit.GroupBy(recs, func(r audit.Record) string {
			if r.KeyID != "" {
				return "" // 有指纹的已经进 byKey，不能重复计
			}
			if strings.TrimSpace(r.User) == "" {
				return "默认"
			}
			return r.User
		}) {
			out.byName[k] = audit.MergeBuckets(out.byName[k], b)
		}
	}
	return out
}

// cloneKeys 复制密钥表。改一份副本再整体保存，避免调用方持有的 map
// 被就地修改（livecfg 快照里的那张表是不可变的）。
func cloneKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func clonePolicies(m map[string]livecfg.KeyPolicy) map[string]livecfg.KeyPolicy {
	out := make(map[string]livecfg.KeyPolicy, len(m))
	for id, policy := range m {
		policy.Models = append([]string(nil), policy.Models...)
		out[id] = policy
	}
	return out
}

// displayKeyName 密钥名的展示回落：空名 → 「默认」。
// 必须与 audit.Summarize 和 httpauth 的口径一致，否则页面上的名字
// 跟「请求审计」里的对不上，用量就永远显示为 0。
func displayKeyName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "默认"
	}
	return s
}
