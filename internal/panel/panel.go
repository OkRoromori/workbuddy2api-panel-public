// Package panel 内嵌式 Web 管理面板：账号池总览、单号运维（解冻/禁用/签到/
// 刷新余额/移除）、浏览器内 OAuth 添加账号（免重启热加载进池）、手动批量
// 签到/保活，以及运行日志环形缓冲（镜像 log 包与 chat 表格日志）。
//
// 设计约束：
//   - 前端 go:embed 单文件（index.html），无任何外部构建依赖，与二进制同体部署；
//   - 鉴权复用网关 api_key（Bearer），与 /v1/* 同一口径；api_key 为空 = 不鉴权
//     （仅本机/私网使用）。面板 HTML 本身无秘密，可匿名加载，密钥只发给 /panel/api/*；
//   - 不改写既有池语义：所有运维操作落到 pool 已有入口（Revive/Disable/Remove...），
//     添加账号走 auth.SaveAtomic + pool.Add，重启后与 auths/ 目录天然对齐。
package panel

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OkRoromori/workbuddy2api-panel-public/internal/audit"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/httpauth"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/livecfg"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/logfmt"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/metrics"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/pool"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/scheduler"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/upstream"
	"github.com/OkRoromori/workbuddy2api-panel-public/internal/usage"
)

// Config 面板依赖（main 装配注入）。
type Config struct {
	Pool      *pool.Pool
	Upstream  *upstream.Client
	Scheduler *scheduler.Scheduler // 手动触发签到/保活；nil 时对应接口返回 501
	AuthDir   string               // OAuth 登录完成后凭证落盘目录
	APIKey    string               // 空 = 不鉴权（与主服务同语义）；与 Live 同时给出时 Live 优先
	RedisMode string               // "upstash" / "noop"，仅观测透出
	Version   string               // 面板版本号（展示用）

	// Live 运行期可变配置（在线改配置立即生效）。
	Live *livecfg.Holder

	// ConfigPath config.json 路径与加载器（配置页读写用）。
	// LoadConfig 返回解析后的配置对象（前端展示/校验用，具体类型由 main 注入的闭包决定）；
	// nil 时配置页返回 501。
	ConfigPath string
	LoadConfig func() (any, error)
	// SaveConfig 校验并落盘配置，返回需要重启才能生效的字段列表；随后由 main 注入的
	// ApplyConfig 闭包完成热生效（池参数/排程/密钥/脱敏）。error 时配置不写盘。
	SaveConfig func(raw []byte) (restartRequired []string, err error)

	// StickyCount 返回粘性会话绑定数；nil 时报告 0。
	StickyCount func() int

	// LoadKeys / SaveKeys 面板「密钥」页的读写通路（main 注入）。
	//
	// 为什么不复用 LoadConfig/SaveConfig：那两个是围绕**整份配置**设计的，
	// 密钥页只想动 api_keys 一个键。走整份配置意味着把 normalize 后的全部字段
	// 一起回写，一旦某个字段的规范化行为和用户手写值不同，保存密钥就会顺手改掉
	// 别的配置——爆炸半径完全不成比例。这里只读改写一个键，热生效也只刷新快照。
	// nil 时密钥页接口返回 501。
	LoadKeys func() (KeysView, error)
	SaveKeys func(extra map[string]string) error
	// SaveKeySettings 与 SaveKeys 配套，原子保存密钥及其模型/并发策略。
	// 新装配应提供本回调；SaveKeys 仅保留给旧测试/嵌入方兼容。
	SaveKeySettings func(extra map[string]string, policies map[string]livecfg.KeyPolicy) error

	// AliasPath 账号别名文件路径（data/aliases.json）。空 = 禁用别名功能
	// （接口返回 501，列表回落原始昵称）。别名纯展示层，见 alias.go。
	AliasPath string

	// Metrics 用量指标聚合器（仪表盘数据源）。nil 时仪表盘接口返回 501，
	// 前端显示「未启用」——不假装有数据。
	Metrics *metrics.Registry

	// Audit 请求审计流水（使用日志页数据源）。nil 时该页接口返回 501。
	Audit *audit.Recorder

	// Restart 请求进程退出以便启动器重新拉起（含源码更新时的重编译）。
	// nil 时接口返回 501（测试/无启动器场景）。
	Restart func()

	// LogsPath 运行日志环形缓冲落盘路径（data/logs.json）。空 = 不落盘。
	LogsPath string

	// TunnelShare 开启"下载朋友连接脚本"。为空时分享接口返回 501。
	TunnelShare TunnelShareConfig

	// Usage 逐请求用量记录器（nil = 用量接口返回 501）。
	Usage *usage.Recorder

	// ProbeFile 模型输出上限探测结果文件（scripts/probe_max_tokens.py --panel-out
	// 写入；空或文件不存在 = model_probes 端点返回空集，面板不显示任何实测标注）。
	// 只读展示：网关不解析、不依赖其内容做任何路由/出站决策。
	ProbeFile string
}

// Panel 管理面板 handler。挂载方式：外层 mux Handle("/panel/", panel)，
// 本 mux 的 pattern 均带 /panel 前缀（外层不做前缀剥离）。
type Panel struct {
	cfg     Config
	mux     *http.ServeMux
	started time.Time
	logs    *Ring

	// logins 进行中的 OAuth 设备授权会话（state → 会话信息）。
	// poll 成功或超时（loginTTL）后剔除；面板常驻进程，容量天然有界。
	loginMu sync.Mutex
	logins  map[string]loginSession

	// taskMu/taskLocks 一键完成任务的 per-account 互斥：同一账号的任务动作
	// （单任务 / 全量）同时只允许一条在跑。重复点击直接返回 409"仍在执行"，
	// 而不是并发跑两遍浪费上游请求（动作虽幂等，expert 系每遍含 8 次真实对话）。
	// 不同账号之间不互斥（并行照旧）。TryLock 语义，锁条目常驻（账号数有界）。
	taskMu    sync.Mutex
	taskLocks map[string]*sync.Mutex

	// 任务中心执行队列（taskcenter.go）。
	queueOnce sync.Once
	q         *queueState

	// tunnelMu 串行化受限 SSH authorized_keys 的轮换与撤销。
	tunnelMu sync.Mutex

	// aliases 账号别名表（uid → 面板显示名）。纯展示层，与池/凭据解耦。
	aliases *aliasStore

	// trendCache 「近 N 天用量趋势」的结果缓存（见 trend.go）。读写都持 trendCacheMu。
	trendCacheMu   sync.Mutex
	trendCacheData trendCacheVal
	trendCacheN    int
	// trendCacheRealm 缓存对应的域（"" = 全部）。缓存键必须带上它：否则切域后会拿错域的
	// 趋势图（表现为"点了开关数字不动"）。
	trendCacheRealm string
}

// trendCacheVal trend 缓存的值部分（独立类型只为字段分组清晰）。
type trendCacheVal struct {
	res     *trendResult
	hasData bool
}

// tryLockAccount 尝试锁定账号的任务执行；已在执行返回 false。
func (p *Panel) tryLockAccount(uid string) bool {
	p.taskMu.Lock()
	if p.taskLocks == nil {
		p.taskLocks = make(map[string]*sync.Mutex)
	}
	mu := p.taskLocks[uid]
	if mu == nil {
		mu = &sync.Mutex{}
		p.taskLocks[uid] = mu
	}
	p.taskMu.Unlock()
	return mu.TryLock()
}

// unlockAccount 释放账号任务锁（与 tryLockAccount 配对）。
func (p *Panel) unlockAccount(uid string) {
	p.taskMu.Lock()
	mu := p.taskLocks[uid]
	p.taskMu.Unlock()
	if mu != nil {
		mu.Unlock()
	}
}

// loginTTL 授权 URL 的最长有效期：超时的 state 直接回收，
// 防止"开了添加账号弹窗就走开"的会话永久滞留。
const loginTTL = 15 * time.Minute

// loginSession 进行中的 OAuth 会话：创建时刻 + realm（cn/global，用于落盘与端点切换）。
type loginSession struct {
	created time.Time
	realm   string // "cn" / "global"，缺省 cn
}

// New 构建面板。
func New(cfg Config) *Panel {
	if cfg.RedisMode == "" {
		cfg.RedisMode = "noop"
	}
	p := &Panel{
		cfg:     cfg,
		mux:     http.NewServeMux(),
		started: time.Now(),
		logs:    NewRing(500),
		logins:  map[string]loginSession{},
	}
	p.routes()
	return p
}

// Logs 返回日志环形缓冲（main 经 MultiWriter 镜像 log 与 chat 表格日志进来）。
func (p *Panel) Logs() *Ring { return p.logs }

func (p *Panel) routes() {
	p.mux.HandleFunc("GET /panel/{$}", p.index)
	p.mux.HandleFunc("GET /panel/app.js", p.appScript)
	p.mux.HandleFunc("GET /panel/api/overview", p.withAuth(p.overview))
	p.mux.HandleFunc("GET /panel/api/logs", p.withAuth(p.logsHandler))
	p.mux.HandleFunc("GET /panel/api/models", p.withAuth(p.models))
	p.mux.HandleFunc("POST /panel/api/login/start", p.withAuth(p.loginStart))
	p.mux.HandleFunc("GET /panel/api/login/poll", p.withAuth(p.loginPoll))
	p.mux.HandleFunc("GET /panel/api/login/regions", p.withAuth(p.loginRegions))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/revive", p.withAuth(p.accountRevive))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/disable", p.withAuth(p.accountDisable))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/checkin", p.withAuth(p.accountCheckin))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/balance", p.withAuth(p.accountBalance))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/probe", p.withAuth(p.accountProbe))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/activate", p.withAuth(p.accountActivate))
	p.mux.HandleFunc("POST /panel/api/accounts/activate_all", p.withAuth(p.accountActivateAll))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/remove", p.withAuth(p.accountRemove))
	p.mux.HandleFunc("GET /panel/api/accounts/{uid}/tasks", p.withAuth(p.accountTasks))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/tasks/accept", p.withAuth(p.accountTaskAccept))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/tasks/accept_all", p.withAuth(p.taskAcceptAll))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/tasks/claim", p.withAuth(p.accountTaskClaim))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/tasks/auto", p.withAuth(p.accountTaskAuto))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/tasks/auto_all", p.withAuth(p.accountTaskAutoAll))
	p.mux.HandleFunc("POST /panel/api/tasks/scan_all", p.withAuth(p.tasksScanAll))
	p.mux.HandleFunc("POST /panel/api/tasks/run_queue", p.withAuth(p.tasksRunQueue))
	p.mux.HandleFunc("GET /panel/api/tasks/queue", p.withAuth(p.tasksQueueStatus))
	p.mux.HandleFunc("GET /panel/api/school/status", p.withAuth(p.schoolStatus))
	p.mux.HandleFunc("POST /panel/api/school/run_all", p.withAuth(p.schoolRunAll))
	p.mux.HandleFunc("GET /panel/api/school/vouchers", p.withAuth(p.schoolVouchers))
	p.mux.HandleFunc("POST /panel/api/checkin_all", p.withAuth(p.checkinAll))
	p.mux.HandleFunc("POST /panel/api/travel_all", p.withAuth(p.travelAll))
	p.mux.HandleFunc("POST /panel/api/activity_all", p.withAuth(p.activityAll))
	p.mux.HandleFunc("POST /panel/api/keepalive_all", p.withAuth(p.keepaliveAll))
	p.mux.HandleFunc("POST /panel/api/balance_all", p.withAuth(p.balanceAll))
	p.mux.HandleFunc("GET /panel/api/packages", p.withAuth(p.packages))
	p.mux.HandleFunc("GET /panel/api/usage", p.withAuth(p.usageDispatch))
	p.mux.HandleFunc("POST /panel/api/usage/save", p.withAuth(p.usageSave))
	p.mux.HandleFunc("GET /panel/api/model_probes", p.withAuth(p.modelProbes))
	p.mux.HandleFunc("GET /panel/api/config", p.withAuth(p.getConfig))
	p.mux.HandleFunc("POST /panel/api/config", p.withAuth(p.saveConfig))

	// —— 本地功能线独有端点（extra_handlers.go / stats.go / trend.go / keys.go）——
	// 仪表盘：进程内指标快照 + 审计流水按天聚合的趋势。
	p.mux.HandleFunc("GET /panel/api/stats", p.withAuth(p.stats))
	p.mux.HandleFunc("GET /panel/api/trend", p.withAuth(p.trend))
	// 密钥管理页（多密钥 + 模型/并发策略）。
	p.mux.HandleFunc("GET /panel/api/keys", p.withAuth(p.keys))
	p.mux.HandleFunc("POST /panel/api/keys", p.withAuth(p.keyCreate))
	p.mux.HandleFunc("POST /panel/api/keys/{id}/rename", p.withAuth(p.keyRename))
	p.mux.HandleFunc("POST /panel/api/keys/{id}/delete", p.withAuth(p.keyDelete))
	p.mux.HandleFunc("POST /panel/api/keys/{id}/share", p.withAuth(p.keyShare))
	// 账号别名。
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/alias", p.withAuth(p.accountAlias))
	// 进程重启（启动器重编译拉起）。
	p.mux.HandleFunc("POST /panel/api/restart", p.withAuth(p.restart))
	// 运维批量动作：验活 / 死号自检 / 解冻 / 移除禁用 / 全池做任务 / 全部领取。
	p.mux.HandleFunc("POST /panel/api/probe_all", p.withAuth(p.probeAll))
	p.mux.HandleFunc("POST /panel/api/dead_audit", p.withAuth(p.deadAudit))
	p.mux.HandleFunc("POST /panel/api/revive_all", p.withAuth(p.reviveAll))
	p.mux.HandleFunc("POST /panel/api/remove_disabled", p.withAuth(p.removeDisabled))
	p.mux.HandleFunc("POST /panel/api/auto_all", p.withAuth(p.autoAllPool))
	p.mux.HandleFunc("POST /panel/api/accounts/{uid}/tasks/claim_all", p.withAuth(p.taskClaimAll))
}

// usageDispatch `/panel/api/usage` 的双口径分发：两条功能线各有一个 "usage" 接口，
// 语义不同、查询参数不同，合并为一个端点按参数分流。
//
//   - 带 hours 参数 → v1.11 的用量记录器快照（逐小时时序 + 按域/账号/模型分桶）；
//   - 不带 hours（或带 date）→ 本地的审计流水日视图（某天的请求明细 + 汇总 + 按人/模型）。
//
// 为什么合并而不是分成两个路径：两个前端（v1.11 面板与本地仪表盘）都在调
// `usage`，各自只带自己那套参数；按参数分流让两边都不用改调用方，也不会出现
// "同一个名字两个端点、谁打错路径静默拿错数据"的坑。
func (p *Panel) usageDispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("hours") != "" {
		p.usage(w, r)
		return
	}
	p.usageDay(w, r)
}

// ServeHTTP 统一入口：先写安全响应头再分发，保证页面、静态资源、API
// 与 401 错误响应全都带上（API 也可能在浏览器里被直接打开）。
func (p *Panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	p.mux.ServeHTTP(w, r)
}

// withAuth 与 server 包同口径的 Bearer 鉴权（经 httpauth 常量时间比较）；
// api_key 为空时放行。密钥经 livecfg 快照读取：面板里改了 api_key，下一个请求
// 即用新值（无需重启）。
func (p *Panel) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpauth.VerifyBearer(r, p.apiKey()) {
			writeErr(w, http.StatusUnauthorized, "invalid_api_key")
			return
		}
		next(w, r)
	}
}

// apiKey 当前生效密钥（Live 优先，回落静态字段）。
func (p *Panel) apiKey() string {
	if p.cfg.Live != nil {
		return p.cfg.Live.Load().APIKey
	}
	return p.cfg.APIKey
}

// ---------------------------------------------------------------------------
// 只读接口
// ---------------------------------------------------------------------------

// realmCountsMapOf 把池的分域计数打包成 JSON 友好的形状（口径与 /status 的 realm_totals 一致）。
func realmCountsMapOf(total, healthy, cooling, disabled, inFlightFull int) map[string]int {
	return map[string]int{
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
	}
}

// overview 总览：池计数 + 每账号状态 + 面板元信息。
func (p *Panel) overview(w http.ResponseWriter, r *http.Request) {
	total, healthy, cooling, disabled, inFlightFull := p.cfg.Pool.CountsDetailed()
	sticky := 0
	if p.cfg.StickyCount != nil {
		sticky = p.cfg.StickyCount()
	}
	accts := p.cfg.Pool.List()
	viewList := make([]accountView, 0, len(accts))
	for _, s := range accts {
		viewList = append(viewList, accountView{Status: s, Alias: p.aliasOf(s.UID)})
	}
	// 面板在线时长以指标聚合器的启动时刻为准（它跨进程重启持久化）——
	// 显示"服务运行了多久"比"面板对象活了多久"更有意义。
	uptime := time.Since(p.started)
	if p.cfg.Metrics != nil {
		if st := p.cfg.Metrics.Started(); !st.IsZero() {
			uptime = time.Since(st)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         p.cfg.Version,
		"uptime_sec":      int(uptime.Seconds()),
		"auth_required":   p.apiKey() != "",
		"redis_mode":      p.cfg.RedisMode,
		"sticky_sessions": sticky,
		"total":           total,
		"healthy":         healthy,
		"cooling":         cooling,
		"disabled":        disabled,
		"in_flight_full":  inFlightFull,
		// realm_totals 分域计数：面板顶部的域开关切到国服/国际服时，这几个数字跟着切。
		// 口径复用池侧同一实现（与 /status 的同名字段一致），避免前端各算一套导致漂移。
		"realm_totals": map[string]any{
			"cn":     realmCountsMapOf(p.cfg.Pool.CountsDetailedForRealm("cn")),
			"global": realmCountsMapOf(p.cfg.Pool.CountsDetailedForRealm("global")),
		},
		// 12153 判死阈值：面板显示「连续 12153 n/阈值」用，避免前端硬编码漂移。
		"session_dead_threshold": pool.SessionDeadThreshold(),
		// 验活判死阈值：面板显示「连续验活失败 n/阈值」用，同样不硬编码。
		"probe_threshold": p.cfg.Pool.ProbeThreshold(),
		"accounts":        viewList,
	})
}

// accountView 账号状态 + 面板展示层的别名覆盖（alias.go）。
//
// 为什么嵌套而不是拍平：别名是纯展示层字段，与池状态正交——这样 v1.11 已有的
// 前端照旧读 s.uid / s.credits / s.disabled（内嵌提升），本地前端额外读 s.alias，
// 两边都不用改。
type accountView struct {
	pool.Status
	Alias string `json:"alias,omitempty"`
}

// aliasOf 取账号别名；别名功能未启用时返回空串。
func (p *Panel) aliasOf(uid string) string {
	if p.aliases == nil {
		return ""
	}
	return p.aliases.get(uid)
}

// logsHandler 返回日志环形缓冲快照（时间升序，含频道标记 chat/task/keepalive/sys）。
//
// 双格式返回：entries（结构化，含 ts/ch，v1.11 前端用）与 lines（纯文本数组，
// 本地功能线的日志页用）。两条前端都在调这个端点，返回两种形态让双方无需改动——
// entries 是权威，lines 是它的文本投影，不会出现两份不一致的数据。
func (p *Panel) logsHandler(w http.ResponseWriter, r *http.Request) {
	entries := p.logs.Snapshot()
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.Text)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"lines":   lines,
	})
}

// models 实时查询上游模型列表与 reasoning 实际档位（直连上游，不读路由层 1h 缓存）：
// 回答"该模型到底支持哪几档思考"。顺带刷新 client 的 effort 降级能力缓存。
// 与 /v1/models 同口径的双域输出：CN 域模型加 "cn:" 前缀、global 域加 "global:" 前缀
// （gateway 路由协议，前端显示的 id 就是调用时要填的完整 model 值）。
// 各域独立探测、独立容错：某域无可用账号则整域跳过；两域全空时才报错
// （有错误明细回 502，一个账号都没有回 503）。
func (p *Panel) models(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	var fetchErrs []string

	// CN 域：有可用 CN 账号才查（此前无条件 Pool.Pick()+FetchModels——选中 global
	// 账号时打 CN 端点必然失败，混合池表现为偶发 502，纯 global 池必炸）。
	if uids := p.cfg.Pool.AvailableUIDsForRealm("cn"); len(uids) > 0 {
		if acct := p.cfg.Pool.AuthByUID(uids[0]); acct != nil {
			infos, err := p.cfg.Upstream.FetchModels(acct)
			if err != nil {
				fetchErrs = append(fetchErrs, "cn: "+err.Error())
			} else {
				for _, mi := range infos {
					out = append(out, panelModelEntry("cn", mi, mi.Efforts, mi.DefaultEffort, p.cfg.Upstream.HTTP))
				}
			}
		}
	}

	// global 域：路由开关开且有可用 global 账号才查（独立目录端点，FetchGlobalModelInfos；
	// Upstream.GlobalEnabled 是探测侧同一道闸，与 main 装配的 config global.enabled 一致）。
	if p.cfg.Upstream.GlobalEnabled {
		if uids := p.cfg.Pool.AvailableUIDsForRealm("global"); len(uids) > 0 {
			if acct := p.cfg.Pool.AuthByUID(uids[0]); acct != nil {
				infos := p.cfg.Upstream.FetchGlobalModelInfos(acct)
				if len(infos) == 0 {
					fetchErrs = append(fetchErrs, "global: 上游未返回可用模型")
				} else {
					efforts, defaults := p.cfg.Upstream.GlobalEffortSnapshot()
					for _, mi := range infos {
						out = append(out, panelModelEntry("global", mi, efforts[mi.ID], defaults[mi.ID], p.cfg.Upstream.HTTP))
					}
				}
			}
		}
	}

	if len(out) == 0 {
		if len(fetchErrs) > 0 {
			writeErr(w, http.StatusBadGateway, "fetch models: "+strings.Join(fetchErrs, "; "))
			return
		}
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号：请先在面板添加账号再查询")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": out})
}

// panelModelEntry 构造单个模型条目（两域共用）：id 带 realm 前缀（调用值即显示值），
// context_length / max_output_tokens 走四级查找链，effort 档位按 realm 域取
// EffortListing（远端权威 ∪ 静态兜底表）——与 /v1/models 同一口径，两侧不再漂移。
func panelModelEntry(realm string, mi upstream.ModelInfo, remoteEfforts []string, remoteDefault string, httpc *http.Client) map[string]any {
	entry := map[string]any{
		"id":                   realm + ":" + mi.ID,
		"name":                 mi.Name,
		"default_effort":       mi.DefaultEffort,
		"supported_efforts":    mi.Efforts,
		"can_disable_thinking": mi.CanDisableThinking,
		"supports_reasoning":   mi.SupportsReasoning,
		"supports_images":      mi.SupportsImages,
		"credits":              mi.Credits,
		"description":          mi.Description,
		"tags":                 mi.Tags,
		"vendor":               mi.Vendor,
		"is_default":           mi.IsDefault,
		"supports_tool_call":   mi.SupportsToolCall,
		"only_reasoning":       mi.OnlyReasoning,
		"reasoning_effort":     mi.ReasoningEffort,
		"reasoning_summary":    mi.ReasoningSummary,
	}
	if mi.MaxAllowedSize > 0 {
		entry["max_allowed_size"] = mi.MaxAllowedSize
	}
	entry["context_length"] = upstream.ContextWindowListingV4(mi.ID, mi.ContextWindow, httpc)
	if mo, ok := upstream.MaxOutputTokensListingV4(mi.ID, mi.MaxTokens, httpc); ok {
		entry["max_output_tokens"] = mo
	}
	if efforts, def := upstream.EffortListing(realm, mi.ID, remoteEfforts, remoteDefault); efforts != nil {
		entry["supported_efforts"] = efforts
		if def != "" {
			entry["default_effort"] = def
		}
	}
	return entry
}

// modelProbes 返回模型输出上限的探测结果（scripts/probe_max_tokens.py --panel-out
// 写入的契约文件），供前端在「模型与档位」的实测列做风险标注。
//
// 设计边界：纯只读透传——文件缺失/未配置返回空集（面板退化为无标注，与历史行为
// 一致），网关自身不解析字段语义、不据此做任何路由或出站决策；上游改了限制后
// 重跑一次工具、下次查询即刷新，无需重启网关。
func (p *Panel) modelProbes(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"probes": map[string]json.RawMessage{}, "exists": false}
	if p.cfg.ProbeFile == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	raw, err := os.ReadFile(p.cfg.ProbeFile)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeErr(w, http.StatusInternalServerError, "read probes: "+err.Error())
		return
	}
	var f struct {
		Version int                        `json:"version"`
		Probes  map[string]json.RawMessage `json:"probes"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		writeErr(w, http.StatusBadGateway, "parse probes: "+err.Error())
		return
	}
	if f.Probes == nil {
		f.Probes = map[string]json.RawMessage{}
	}
	out["probes"] = f.Probes
	out["exists"] = true
	if fi, err := os.Stat(p.cfg.ProbeFile); err == nil {
		out["updated_at"] = fi.ModTime().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// 账号运维
// ---------------------------------------------------------------------------

// accountRevive 手动复活：清禁用 + 冷却 + 熔断（运维口径无条件恢复）。
func (p *Panel) accountRevive(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if _, ok := p.cfg.Pool.Status(uid); !ok {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	p.cfg.Pool.Revive(uid)
	log.Printf("panel: revive uid=%s（人工清除禁用/冷却/熔断）", uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// accountDisable 人工禁用（不再参与选号，需面板 revive 或重登恢复）。
func (p *Panel) accountDisable(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if _, ok := p.cfg.Pool.Status(uid); !ok {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	p.cfg.Pool.Disable(uid, "manual disable (panel)")
	log.Printf("panel: disable uid=%s（人工禁用）", uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// accountCheckin 单号签到：DailyCheckin + 余额查询解冻（已签到等业务错误不阻塞余额刷新），
// 与 scheduler.RunCheckinNow 的单号语义一致。
func (p *Panel) accountCheckin(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.cfg.Pool.AuthByUID(uid)
	if a == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	checkinMsg := ""
	if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
		checkinMsg = err.Error() // "今天已签到"等业务错误照常查余额
	}
	resp := map[string]any{"ok": true}
	if checkinMsg != "" {
		resp["checkin_message"] = checkinMsg
	}
	remain, total, err := p.cfg.Upstream.UserResource(a)
	if err != nil {
		resp["balance_error"] = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	p.cfg.Pool.ReenableIfCredits(uid, remain, total)
	resp["credits"] = remain
	resp["credits_total"] = total
	log.Printf("panel: checkin uid=%s msg=%q credits=%d/%d", uid, checkinMsg, remain, total)
	writeJSON(w, http.StatusOK, resp)
}

// accountProbe 单号验活（账号页行内「验活」按钮）：真发一次极小请求，看这个号到底还行不行。
//
// 判定与记账跟定时/批量验活完全同源（scheduler.ProbeOne → pool.NoteProbe），所以按钮给出的
// 结论就是调度器会给出的结论，不存在"面板说好、调度器判死"的分裂。同步返回（一次极小请求
// 约 1-3 秒），失败原因原样透出——用户要能分辨"号真坏了"和"上游正在限流"。
func (p *Panel) accountProbe(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	ok, errText, autoDisabled, err := p.cfg.Scheduler.ProbeOne(uid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := map[string]any{"ok": ok, "auto_disabled": autoDisabled}
	if !ok {
		resp["error"] = errText
	}
	writeJSON(w, http.StatusOK, resp)
}

// accountBalance 单号余额刷新：UserResource → SetCredits（不触碰冷却状态）。
func (p *Panel) accountBalance(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.cfg.Pool.AuthByUID(uid)
	if a == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	remain, total, err := p.cfg.Upstream.UserResource(a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user resource: "+err.Error())
		return
	}
	p.cfg.Pool.SetCredits(uid, remain, total)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credits": remain, "credits_total": total})
}

// accountActivate 国际服「一键激活」：补地区 → 注册激活 → 领 trial → 等入账 → 读余额。
//
// 为什么要有这个按钮：这套链路本来只藏在「添加账号」的登录流程里、且没有可见结果——积分是 0
// 时用户分不清是没激活、trial 没领到、还是刚领的积分还没入账（实测入账延迟约 1 秒）。
// 现在可以随时重跑，并逐步看到卡在哪一步。
func (p *Panel) accountActivate(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.cfg.Pool.AuthByUID(uid)
	if a == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	if a.Realm() != "global" {
		writeErr(w, http.StatusBadRequest, "只对国际服账号适用（国内服没有这套激活链路）")
		return
	}
	steps, remain, total, err := p.cfg.Upstream.ActivateGlobal(a)
	resp := map[string]any{"ok": err == nil, "uid": uid, "steps": steps}
	if err != nil {
		resp["error"] = err.Error()
	}
	if err == nil {
		p.cfg.Pool.SetCreditsDetailed(uid, remain, total, -1)
		resp["credits"] = remain
		resp["credits_total"] = total
	}
	log.Printf("panel: 激活国际服 uid=%s %s", logfmt.Label(uid, a.Nickname), upstream.StepsSummary(steps))
	writeJSON(w, http.StatusOK, resp)
}

// accountActivateAll 对全部国际服账号跑一遍激活，逐个返回结果。
// 单个失败不影响其它号（激活本身幂等，重复点没有副作用）。
func (p *Panel) accountActivateAll(w http.ResponseWriter, r *http.Request) {
	type result struct {
		UID      string                    `json:"uid"`
		Nickname string                    `json:"nickname,omitempty"`
		OK       bool                      `json:"ok"`
		Credits  int64                     `json:"credits"`
		Steps    []upstream.ActivationStep `json:"steps,omitempty"`
		Error    string                    `json:"error,omitempty"`
	}
	accounts := p.cfg.Pool.List()
	out := make([]result, 0)
	activated, failed := 0, 0
	for _, s := range accounts {
		a := p.cfg.Pool.AuthByUID(s.UID)
		if a == nil || a.Realm() != "global" {
			continue
		}
		res := result{UID: a.UID, Nickname: a.Nickname}
		steps, remain, total, err := p.cfg.Upstream.ActivateGlobal(a)
		res.Steps = steps
		if err != nil {
			res.Error = err.Error()
			failed++
		} else {
			p.cfg.Pool.SetCreditsDetailed(a.UID, remain, total, -1)
			res.OK = true
			res.Credits = remain
			activated++
		}
		out = append(out, res)
	}
	log.Printf("panel: 批量激活国际服 %d 个（成功 %d / 失败 %d）", len(out), activated, failed)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": out, "count": len(out), "failed": failed})
}

// accountRemove 移除账号：先出池（立即落盘 state），再删 auth 文件。
func (p *Panel) accountRemove(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.cfg.Pool.Remove(uid)
	if a == nil {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	fileMsg := ""
	if a.FilePath != "" {
		if err := os.Remove(a.FilePath); err != nil && !os.IsNotExist(err) {
			fileMsg = err.Error()
		}
	}
	if fileMsg != "" {
		log.Printf("panel: remove uid=%s（auth 文件删除失败: %s）", uid, fileMsg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file_error": fileMsg})
		return
	}
	log.Printf("panel: remove uid=%s（已出池并删除凭证文件）", uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 批量任务
// ---------------------------------------------------------------------------

// checkinAll 手动触发全量签到（异步执行，进度看日志区/账号状态变化）。
func (p *Panel) checkinAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	go p.cfg.Scheduler.RunCheckinNow()
	log.Printf("panel: 手动全量签到已触发（含猫猫旅行）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// travelAll 手动触发全量猫猫旅行巡检（异步执行）。
func (p *Panel) travelAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	go p.cfg.Scheduler.RunTravelNow()
	log.Printf("panel: 手动全量旅行巡检已触发")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// activityAll 手动触发全量活跃上报（异步执行；点亮连登 + 解锁领养前置）。
func (p *Panel) activityAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	go p.cfg.Scheduler.RunActivityNow()
	log.Printf("panel: 手动全量活跃上报已触发")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// keepaliveAll 手动触发全量 token 保活（异步执行）。
func (p *Panel) keepaliveAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	go p.cfg.Scheduler.RunKeepaliveNow()
	log.Printf("panel: 手动全量保活已触发")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

// balanceAll 手动全量刷新余额：并发查上游、写回池内 credits（含解冻语义），
// 完成后返回——面板紧接着拉 overview 即是最新值。账号量小（个位数），
// 同步等待（上限受短 RPC 超时约束）比"触发后盲刷"体验更确定。
func (p *Panel) balanceAll(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Scheduler == nil {
		writeErr(w, http.StatusNotImplemented, "scheduler not available")
		return
	}
	p.cfg.Scheduler.RunBalanceRefreshNow()
	log.Printf("panel: 手动全量余额刷新完成")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": p.cfg.Pool.List()})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// usage 返回逐请求用量聚合。hours 查询参数控制小时粒度时序窗口（默认 72，
// 上限 1440=60 天）；更早的数据自动折叠为日点，因此长期趋势不会丢。
func (p *Panel) usage(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Usage == nil {
		writeErr(w, http.StatusNotImplemented, "usage recorder not available")
		return
	}
	hours := 72
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	if hours > 1440 {
		hours = 1440
	}
	// 昵称仅用于展示，取自池快照（不含任何凭证）。
	nicks := map[string]string{}
	for _, s := range p.cfg.Pool.List() {
		if s.Nickname != "" {
			nicks[s.UID] = s.Nickname
		}
	}
	writeJSON(w, http.StatusOK, p.cfg.Usage.Snapshot(hours, nicks))
}

// usageSave 立即把内存中的用量桶落盘（正常由后台 30s 防抖刷新负责）。
func (p *Panel) usageSave(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Usage == nil {
		writeErr(w, http.StatusNotImplemented, "usage recorder not available")
		return
	}
	p.cfg.Usage.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// packages 返回全部账号的积分包构成，供「积分构成」视图对比。
//
// 逐个账号向上游查（并发有上限，避免瞬时打满上游限流），失败只在对应账号上
// 标 error，不影响其它账号——一个号 token 失效不该让整页空白。
func (p *Panel) packages(w http.ResponseWriter, r *http.Request) {
	accts := p.cfg.Pool.List()
	type row struct {
		UID      string                   `json:"uid"`
		Nickname string                   `json:"nickname"`
		Realm    string                   `json:"realm"`
		Remain   int64                    `json:"remain"`
		Size     int64                    `json:"size"`
		Packages []upstream.CreditPackage `json:"packages"`
		Error    string                   `json:"error,omitempty"`
	}
	out := make([]row, len(accts))

	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, s := range accts {
		wg.Add(1)
		go func(i int, s pool.Status) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			it := row{UID: s.UID, Nickname: s.Nickname, Realm: s.Realm}
			a := p.cfg.Pool.AuthByUID(s.UID)
			if a == nil {
				it.Error = "account not loaded"
				out[i] = it
				return
			}
			packs, remain, size, err := p.cfg.Upstream.CreditPackages(a)
			if err != nil {
				it.Error = err.Error()
				out[i] = it
				return
			}
			it.Packages = packs
			it.Remain = remain
			it.Size = size
			out[i] = it
		}(i, s)
	}
	wg.Wait()

	// 余额降序：多的在前，便于和少的对比。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Remain > out[j].Remain })
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}
