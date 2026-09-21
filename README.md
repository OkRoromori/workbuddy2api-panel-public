<p align="center">
  <img src="https://raw.githubusercontent.com/DGZSbot/ai-icon/refs/heads/main/WorkBuddy.png" alt="WorkBuddy2API" width="120">
</p>

<h1 align="center">WorkBuddy2API Panel</h1>

<p align="center">
  <b><a href="https://github.com/linguo2625469/workbuddy2api-panel">linguo2625469/workbuddy2api-panel</a> 的增强分支 · Anthropic + Responses 双协议 · 多密钥 · 账号验活 · 请求审计</b>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.22.5-00ADD8?logo=go&logoColor=white&style=flat-square">
  <img alt="API" src="https://img.shields.io/badge/API-OpenAI_Compatible-412991?style=flat-square">
  <img alt="Deploy" src="https://img.shields.io/badge/Deploy-Single_Binary%20%7C%20Docker-2496ED?style=flat-square">
  <img alt="Transport" src="https://img.shields.io/badge/Transport-SSE%20%2F%20Streaming-0DBD8B?style=flat-square">
</p>

---

**中文** | [English](README.en.md)

> ⚠️ **本仓库是 fork，而且是三层。每层的贡献必须分清——尤其别把下面两层的算成本分支的。**
>
> **① 更上游的源头 [`Sliverkiss/workbuddy2api`](https://github.com/Sliverkiss/workbuddy2api)（作者 Sliverkiss）** —— 网关内核：账号池调度、熔断与冷却、会话粘性、错误分类、提示词体系、指纹脱敏、定时任务、OAuth 登录、积分任务链路。形态是**纯命令行 + 脚本**。克隆其**全部历史**核实（464 个提交、4 个分支）：**从来没有过 Web 界面**——唯一的 `.js` 文件是 `.github/actions/ai-governance` 那套机器人，两个 `go:embed` 目标是 `defaultprompt.md` 与 `model.json` 数据文件，全历史没有 `panel/` `webui/` 目录。
>
> **② 原项目（本仓库直接基于） [`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel)（作者 linguo2625469）** —— 在 ① 的内核之上**做出了 Web 管理面板**（`internal/panel`，7 个视图，go:embed 进二进制），以及面板内双域 OAuth 添加账号、任务中心、连登/开学季自动化、在线配置编辑、首启生成配置、余额后台刷新、`httpauth` 常量时间校验与 CSP、模型输出上限探测等。**本仓库是从这个仓库起步改的——面板本体属于这一层，不是本分支的增量。**
>
> **③ 本仓库** —— 相对 ② 的增量见[下节](#-本分支改了什么)。
>
> 基础功能（配置字段、账号池语义、上游错误分类）看 ① 的 README；面板用法看 ② 的 README。

## 📌 来源声明

| 项 | 内容 |
|---|---|
| ① 更上游的源头 | [`Sliverkiss/workbuddy2api`](https://github.com/Sliverkiss/workbuddy2api)，作者 **Sliverkiss** —— 网关内核、协议适配、错误分类、提示词体系、账号池设计的出处，**它的 README 才是基础功能的权威文档** |
| ② 原项目（本仓库直接基于） | [`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel)，作者 **linguo2625469** —— 在 ① 之上做出 **Web 管理面板与面板侧运维层**；**本仓库就是从它 fork 的**；基座 v1.11.0-panel（`b4245a8`），已吸收到 `b69d06e`（1.11.1-panel） |
| ③ 本仓库 | [`OkRoromori/workbuddy2api-panel-public`](https://github.com/OkRoromori/workbuddy2api-panel-public) —— ② 的增强分支，增量见下节 |
| 许可 | 继承 MIT License，**三层版权声明都保留**（[LICENSE](LICENSE) 顶部三行 Copyright） |
| 引用要求 | 再分发（源码或二进制）时保留 ① 与 ② 的 MIT 版权与许可声明，并注明原始出处 `https://github.com/Sliverkiss/workbuddy2api` |

- 若你是从本仓库认识这个项目的：**底子是 ① 和 ② 打的，建议先去这两个仓库点个 Star**。
- 遇到**网关内核**层面的问题（账号池怎么选号、错误怎么分类、配置字段什么含义），① 的 README 与实现才是权威解释。
- 本仓库的增量改动由本仓库维护者负责，与 ① ② 的作者无关。

## 🆚 本分支改了什么

**基准是 ② 原项目（本仓库直接基于它 fork），不是 ①。** 本分支以 ② 的 `v1.11.0-panel`（`b4245a8`）为基座，把本地这条线独有的能力移植上去，形成单一分支。

标记含义：

- `🆕` = **① 和 ② 都没有**，本分支新增
- `🔧` = **② 已有同一能力**，本分支改造 / 扩展 / 补出入口

> **比对方法**（2026-09-21）：克隆 ①（464 个提交，tip `d1023f3`）与 ②（70 个提交，tip `b69d06e`）后逐文件比对。② 有 **139 个文件**，其中 **104 个本仓库原样沿用**、**35 个实质改动**（模块路径那一行的差异不算改动）；本仓库另有 **51 个新增文件**。下表每一条都按这个差集写，且都能在 ② 的仓库里反查。

| | 能力 | 说明 |
|---|---|---|
| 🆕 | **Anthropic + Responses 双协议** | ① 只暴露 OpenAI 兼容，② 也只有 OpenAI 兼容（`internal/server` 下没有 `anthropic*.go` / `responses*.go`）。本分支新增 `POST /v1/messages`（Anthropic，认 `x-api-key`，Claude Code / ccswitch 可直接接）与 `POST /v1/responses`（Codex，含流式事件翻译）。配套改 `httpauth`：② 只认 `Authorization: Bearer`，本分支加 `x-api-key` 回落（Anthropic 协议只发这个头） |
| 🆕 | **多密钥体系 + 密钥页** | ① 与 ② 都只有单个 `api_key`（配置里没有 `api_keys`）。本分支：每把密钥独立用量、模型白名单（按**裸名**比对）、单密钥并发上限、给朋友生成专用连接脚本；`/v1/models` 按该密钥白名单过滤；`/status` 收紧为 **owner 密钥专用**（② 是普通 `withAuth`，任何密钥都能读到账号池明细） |
| 🆕 | **账号验活（probe）** | ② 里的 "probe" 指的是**模型输出上限探测**（`scripts/probe_max_tokens.py --panel-out`），**没有账号存活探测**。本分支真发一次极小请求判断该号还能不能干活；判定与记账与定时/批量验活**同源**（连续失败达阈值自动禁用，与 `NoteSessionDead` 同哲学），不存在"面板说好、调度器判死"的分裂 |
| 🆕 | **审计流水 / 指标聚合 + 独立仪表盘视图** | ① 没有 `internal/audit` / `internal/metrics`，② 也没有（② 的「用量」页只读内存计数）。本分支新增审计流水（JSONL，默认保留 14 天）、按日明细（账号 / 模型 / token / 积分，可按域与账号筛选）、域级积分采样（余额 / 消耗 / 净速率 / 到期批次，**落盘**重启不丢），以及独立的 `dashboard` 视图（KPI、按模型、按密钥、趋势、积分口径） |
| 🆕 | **国服 / 国际服域视图开关** | ② 有 CN/Global 的**登录**区分与 `realm` 骨架（`internal/pool/realm.go`），但没有"看哪些数据"的域开关（无 `realm_filter.go`，界面里也没有"国服 / 国际服"字样）。本分支顶栏三态开关（全部｜国服｜国际服，存浏览器），账号 / 模型 / 积分构成 / 审计 / 仪表盘 / 密钥用量列 / 添加账号默认域全部跟随。**只影响看哪些数据，不参与请求路由**；国际服模式下国内服专属页显示说明且不发请求 |
| 🔧 | **模型域路由：`model_realm` + 裸名下发** | ② 的域靠**模型名前缀**表达（`/v1/models` 下发 `cn:xxx`，`resolveModel` 认 `cn:` / `global:`），配置里没有 `model_realm`。本分支改为下发**裸名**（客户端拉到即可用），域由新增的 `model_realm` 解析：显式前缀 > 目录事实（只有单域有的模型强制该域）> 逐模型 `pins` > 全局 `prefer`；软默认在目标域无可用账号时**跨域回落**，硬指定（前缀 / pin）不回落。面板模型页「重复模型」chip 里可逐个钉 |
| 🔧 | **模型目录双 UA 探测** | ② 已经在**两个端点**之间做并集（`/v3/config` 主 + 企业端点家族，并发 + 单路降级），但探 `/v3/config` 只用一种 UA。本分支发现该端点对 **UA 敏感**：IDE UA 与 CLI UA 的目录**互补**（第三方模型族只在 CLI 目录下发，IDE 目录只有内部/工具族）。两路取并集后实测目录 34 → 57 个（`deepseek-v4.1-flash` 免费档、`gpt-6-astra` 等入列）；单路失败仍用另一路 |
| 🔧 | **国际服一键激活** | ② 在**登录时**自动完成 global 的注册地区 / 激活 / trial 领取（`internal/panel/login.go` 的 realm 分支），但没有对**已入池账号**单独触发的入口。本分支补：账号行「激活」+ 工具栏「激活国际服」，逐步返回结果。顺带修掉"激活后读余额为 0"：trial 是**异步记账**的（实测调用与积分包 `CreateTime` 相差约 1 秒），此前紧跟其后读余额会读到 0 |
| 🔧 | **面板改造** | ② 的面板是 7 个视图（accounts / usage / packages / taskscenter / models / config / logs）。本分支：新增 `dashboard` / `keys` / `checkin` **三个视图**（共 10 个），静态资源加内容指纹 `ETag` + `Cache-Control: no-cache`（部署后**普通刷新**即见新版，此前要硬刷新）、日志新增 `keepalive` 频道并把环形日志**落盘**（② 只有 chat/task/sys 三频道且只在内存）、修掉面板自身三处缺陷（`.sw` 类名撞车、账号表整行挂 `title`、文案错字） |
| 🆕 | **路径写法容错** | `internal/server/paths.go`：`/v1/v1/models`、`/models`、尾斜杠等写法都兜住（① ② 都没有这个文件，客户端多拼或少拼一个 `/v1` 就 404） |
| 🆕 | **升级脚本** | `upgrade-run.sh` / `deploy/upgrade-run.sh`：指纹校验 → 属主检查 → 备份（只留最近 1 个）→ 原子替换 → 重启 → 冒烟检查（`/healthz` `/status` `/panel/api/usage`）→ 打印回滚命令 |
| 🆕 | **密钥分享的服务端配套** | 面板 `internal/panel/tunnel_share.go` 生成的朋友脚本**内嵌一把 Ed25519 私钥**，配合服务器上的受限账号（`wb2tunnel`，只允许转发 `127.0.0.1:7863`，不能执行命令、不能开 TTY）；每次点「分享」都会轮换密钥，删密钥同时撤销隧道授权 |

### 细项清单（除上表外，还改了这些）

**接口面**
- 🆕 **`/v1/models` 下发裸名 + `realm` 字段 + 按密钥白名单过滤**（② 下发带前缀的名字、且不过滤白名单）
- 🆕 **`/status` 收紧为 owner 密钥专用**：访客密钥访问返回 401
- 🔧 **`httpauth` 双认证头**：`Authorization: Bearer` 之外认 `x-api-key`（② 只认前者）

**数据口径**（② 的用量页按"积分 / 千 token"，且占比相对最大模型）
- 🆕 单价口径改成「**积分 / 百万 token**」（`per_mtoken`）；全局值按**累计加权平均**（累计积分 ÷ 累计 token），不是逐请求平均
- 🆕 按模型表新增「积分/Mtok」列；占比改成**占全池合计**（与 donut 卡同口径）

**日志与可观测**
- 🔧 ② 的日志视图已有 chat / task / sys 三频道；本分支新增 **keepalive** 频道（保活 / 验活不再混进系统频道）与**落盘持久化**（`ring_persist.go`，重启不丢）
- 🆕 请求审计视图与域级采样（见上表）

### 修复

**第一批（2026-09-20 实机定位）**——两处都是"客户端看到 503、每次重试稳定复现、换号也无效"的症状，根因都在出站侧，且都用**线上对照实验**验证过（同一份请求、只改一个变量）。

| 缺陷 | 根因 | 修复 | 对照实验 |
|---|---|---|---|
| 上游 `11148`（`tool_call_sequence_broken`）顶死整条会话 | Responses 协议把**并行工具调用**发成多条独立 `function_call` item，翻译层为每条生成一条 assistant 消息，出站成 `assistant(c1) assistant(c2) tool(c1) tool(c2)`；上游 deepseek 系模型要求"声明 tool_calls 的 assistant 之后必须紧跟它自己的结果"，违反即拒 | 出站管线第一步新增 `mergeAdjacentToolCalls`：把背靠背的两条合并成一条（`internal/upstream/tool_pairing.go`）。② 的管线只有 `repackToolResultBlocks` + `cleanupOrphanToolCalls` 两步，没有这一步 | 拆开 = 503/11148；合并 = 200（同模型同内容）。glm 系模型对此宽容，故此前只在 deepseek 上暴露 |
| 上游 `11101`（`Unmarshal chat params failed: unexpected EOF`） | 鉴权层为读 `model` 字段按 **1 MiB** 上限预读请求体，并把**截断后**的内容回填 `r.Body`——长会话（> 1 MiB）因此被砍成半截 JSON 照原样转发（这处缺陷出在本条功能线上，② 的 `handler.go` 里没有预读） | 改为整体读入 + 超 64 MiB 显式 413，绝不静默截断（`internal/server/handler.go`） | 1.4 MiB 合法请求：修前 503/11101，修后 200 |
| 面板静态资源被浏览器缓存住 | 静态资源没有缓存校验器，部署新版本后浏览器仍渲染旧面板 | 内容指纹 `ETag` + `Cache-Control: no-cache`：指纹一致 304、变则回全文（`internal/panel/index.go`） | 修后刷新即拿到新构建 |

**两条结论值得记下**：① 客户端报 503 时，根因可能在我们**出站请求体**而不是上游——先用对照实验隔离，别急着怪账号池；② "长请求被截断成半截 JSON"这类症状，如果出在**我们这侧的大小上限**上，换多少账号都不会好。

**第二批（2026-09-21 代码审查）**——接 agent 客户端时最容易踩的一批，同样是"整条会话报废或静默出错"级。

| 缺陷 | 根因 | 修复 |
|---|---|---|
| **翻译层吞掉上游 error 帧**（Anthropic / Responses 流式） | 两个翻译器只认 `choices`，上游以 SSE `error` 帧下发的 6004 限流、内容拦截被**静默丢弃** → agent 收到"成功的空回复"，既没内容也不知道为什么（OpenAI 直连路径本来是透传的，只有 agent 客户端受害） | Anthropic 发 `error` 事件；Responses 收尾改发 `response.failed`（`status:"failed"` + error 对象）。`StreamHint` 新增 `WithErrorFrameObserver` 旁路观察者（② 的 `StreamHint` 没有 opts 位），透传字节不变 |
| **流式"成功"判定过早** | 读第一帧之前就 `NoteSuccess` + 清 11102 负缓存 + 绑粘性；上游「200 + error 帧」（如 6004 限流）时，限流号被记成健康号，粘性还会把整个会话钉在它身上，后续每一轮都失败 | 成功判定与粘性绑定一律**延后到这一跳真成功之后**；有 error 帧时按帧内容分类（`FrameKind`）施加账号处置 |
| **上游超时白换号** | 全仓**没有超时识别**——超时和"网络抖动"共用同一条换号路径，注定超时的请求会轮转 `MaxRotate` 次，配 600s 的 `header_timeout` 最坏挂约半小时，期间还给一串健康号喂连败计数（注：本地文档曾记"超时不换号已修"，实际代码里没有，本次补上） | 新增 `isUpstreamTimeout`：`net.Error.Timeout` / 显式 deadline / **客户端仍在却被空闲看门狗掐流**三态；命中即止损——不轮转、不罚号，末端返回可区分的 `upstream_timeout` |
| **Responses 非流式 item 顺序** | `function_call` 原排在 `message` 之前，客户端回放后形成 `assistant(工具) → assistant(正文) → tool`，工具结果被一条 assistant 隔断 = 11148 形态，而出站三步管线都修不了 | item 顺序改为 **`message` 在前**（与 OpenAI 自身一致）；出站再补一条安全网：把"带工具调用无正文 + 紧随纯正文"折叠成一条 `assistant(正文 + tool_calls)` |
| 脱敏漏 `reasoning` 字段 | thinking 回填会把客户端送来的 `reasoning_content` 镜像进 `reasoning`，而脱敏只洗 `content` / `reasoning_content` / `tool_calls` → 裸 `11-128` 从这条路径原样出站（该串出现即整单拦截） | `reasoning` 与 `reasoning_content` 同等脱敏 |
| `content: []` 不算"空正文" | 有客户端把"没有正文"发成空数组，背靠背的并行工具调用因而不合并 → 又是 11148 | 空数组也算空，正常触发合并（非空数组仍视为有内容，不丢数据） |
| 密钥并发上限热改成 0 时名额不归还 | 归还时按**释放那一刻**的配置上限判断，上限为 0 直接 return → 该密钥名额永久残留，上限调回后所有请求 429，必须重启进程 | 归还按**取号时**的判定，不再看当前配置 |

这一批每处都补了测试（含 error 帧两协议各一、超时判定七例、item 顺序、正文折叠），`go vet` / `go test ./...` / 前端 harness 全绿。

### 与 ①（更上游源头）的同步

② 已经吸收过 ① 的大部分改动，本分支继承；下面记录的是**本分支这条线**自己做的同步轮次：

- **第一轮（fork 基线 `53ee3a1` → `9a87758`，34 个提交）**：四类任务独立排程、pool 文件拆分、12153 连续计数才禁用、429 `code=6004` 模型级限流收窄、11101 不罚号、请求体 413、DeepSeek 思维链、reasoning_content 回填、Codex 指纹脱敏、系统提示词体系、出站 UA 可配等。
- **第二轮（`9a87758` → `ea8b1e5`，2026-09-14，只吸收底层）**：净化增强（`tool_calls.arguments` 盲区、裸 `11-128` 反探测改写、桌面版身份句漏网）、出站头族对齐官方三段式 UA 与 `X-IDE-*` / `X-Device-Token` / `X-Agent-Purpose`、并发修复（客户端 IP 按请求参数传递）、签到幂等识别、粘性按模型判活（6004 后换模型自动解绑）、report 支持独立 `requestID`。
- 未吸收（明确不做）：① 的 Python 脚本体系（`scripts/task_runner.py` 等，本分支用面板 + 纯 API 实现）、`governance`/CI 工作流、`.github/` 那套 issue 治理机器人。

### 与 ② 原项目的同步状态

本分支以 ② 的 `v1.11.0-panel`（`b4245a8`，2026-09-19）为基座，**已吸收 ② 到 `v1.11.1-panel`（`b69d06e`，2026-09-20）的全部修复**：

| ② 的提交 | 内容 | 本分支 |
|---|---|---|
| `08752df`（09-20） | `fix(panel)`: run_queue 建队合并 mp 口径待办——修「扫描显示待办但执行队列报无可执行」 | **已吸收**。把扫描与执行队列两处口径收成一个 `mergeMPPending`（同一份实现，不再各写一遍），并补回归测试 `internal/panel/taskcenter_test.go`：去掉调用即复现原症状「无可执行待办（全部账号任务已完成）」 |
| `b69d06e`（09-20） | 版本号 1.11.1-panel | 已跟随：`appVersion = "1.11.1-panel"`（表示已吸收 ② 1.11.1 的全部修复，本分支自己的增量不体现在这个号上） |

> 注：本分支在若干点上与 ② 走的是**不同实现**（如大小上限：② 走"移除预拦截、交给上游自然响应"，本分支走"整体读入 + 64 MiB 显式 413"；如模型名：② 下发前缀，本分支下发裸名）。这些不是"落后"，是分叉，见上文各自条目。

### ⚠️ ② 原项目有、本分支只是沿用（别记成本分支的增量）

下面这些**都在 ② 里**（写文档、对外介绍、答辩时别算成本分支的功劳）：

| 能力 | ② 的实现 |
|---|---|
| **Web 管理面板本体** | `internal/panel`，7 个视图（账号 / 用量 / 积分构成 / 任务中心 / 模型 / 设置 / 日志），go:embed 进二进制；明暗主题、账号表健康色条与积分量条、`esc()` 前端转义、CSP + `X-Frame-Options` + `nosniff` |
| **面板内双域 OAuth 添加账号** | `internal/panel/login.go`：按钮发起 → 弹窗选**国内版（CN）/ 国际版（Global）** → 自动轮询识别完成 → 落盘后直接 `Pool.Add`；global 走注册地区 + 激活 + trial 领取 |
| **任务中心视图** | `taskscenter` 视图 + `internal/panel/taskcenter.go`：全账号任务扫描、执行队列（账号内串行、账号间可选并发）、开学季状态矩阵、日志分频道 |
| **积分任务一键完成 / 连登管家** | `internal/panel/tasks.go`、`internal/upstream/streak.go`（连登档位兑换 + 抽奖）、开学季与校园日闭环、桌面端任务指纹逆向 |
| **在线配置编辑（热生效）** | `config` 视图 + `internal/livecfg/livecfg.go`：深合并 + 原子替换，热字段立即生效、装配期字段提示重启 |
| **首启自动生成配置** | `cmd/server/config.go` 的 `WriteDefault` + `main.go` 的 `-config` 说明（不存在时自动生成推荐配置） |
| **余额后台刷新** | `schedule.balance_refresh_enabled` / `balance_refresh_minutes`（默认 5 分钟）+ 冷却账号余额恢复自动解冻 |
| **`httpauth` 与安全加固** | `internal/httpauth`：SHA-256 摘要 + `subtle.ConstantTimeCompare` 常量时间校验；CSP / 安全响应头；面板前端属性转义 |
| **日志分频道** | `internal/panel/ring.go` 的 chat / task / sys 三频道与频道筛选 |
| **领养（旅行）report 前置修复** | `internal/panel/autotask.go` 里 `ReportChatActivity` 已在 `BuddyAgreement` / `BuddyFirst` 之前（本分支的 `travel.go` 与 ② **逐字节相同**，只差模块路径） |
| **带前缀模型名 + realm 骨架** | `/v1/models` 下发 `cn:` 前缀、`resolveModel` 前缀解析、`internal/pool/realm.go` 的 `AvailableUIDsForModelRealm` |
| **模型输出上限探测** | `scripts/probe_max_tokens.py --panel-out` + `/panel/api/model_probes` 端点，模型页叠加实测上限与钳制告警 |
| **模型目录两路并集** | `internal/upstream/global_models.go`：`/v3/config` 主路 + 企业端点家族并发探测、单路降级 |
| **`internal/usage` 用量记账、`internal/logfmt` 日志格式、WAF 治理、连败降权、成本台账、客户端断开取消上游、6004 粘性豁免** | ② 的 `internal/usage`、`internal/logfmt`、`internal/upstream/waf*`、`internal/pool/*` 等；本分支原样沿用 |

### ⚠️ ① 有、② 和本分支都没有

克隆 ① 全历史比对后确认的**遗漏项**（① 有，② 与本分支都没有）：

| ① 的能力 | ① 的实现 | 本分支现状 |
|---|---|---|
| **auths 目录热加载** | `internal/pool/watch.go` + `cmd/server/main.go` 的 `StartAuthDirWatch`：往 `auths/` 丢凭证文件，5s 内自动进池，免重启 | **没有**（本项目只在启动时对齐目录；面板添加账号是即时的，手工丢文件要重启） |
| **账号临时停用 / 恢复** | `manual_disabled` 双位语义 + `/admin/accounts/{uid}/{disable,enable,revive}` + `cmd/acct` CLI（停用期间签到/保活照常） | **没有**（面板的「禁用/移除」是另一套语义） |
| **终端状态面板** | `cmd/stats`（原地刷新的 TUI，含终端尺寸适配） | 对应物是网页管理面板（② 做的） |
| **活跃上报 CLI** | `cmd/activity` | 对应物是 `scripts/probe_active.py` + 面板按钮 |

### 未做 / 待办

| 状态 | 事项 | 说明 |
|---|---|---|
| ✅ 已修复 | single 类任务奖励领取 | 正确端点是 Web 域 `POST https://www.workbuddy.cn/activity/growth/tasks/<task_code>/claim`（任务码在路径、无 body、`x-client-platform: web`）；此前误用 CLI 域导致长期 400。现已**达标即自动领奖**，实测 +100 分 +5 能到账、重复领取幂等 |
| ✅ 已破解 | 桌面端 / 交互类任务 | 通过客户端指纹逆向（`/v2/report` 三通道 + 判据事件载荷），17/18 任务可纯 API 一键完成，多账号实测点亮 |
| ⚠️ 不支持 | 剩余 1 个任务 | `Expert_Philanthropy` 需真实捐款（服务端领奖时校验捐赠回执，已实测无法绕过）；面板展示指引 |
| ❌ 未做 | 面板侧 Upstash / 凭证目录配置 | 涉及启动期装配，需手工编辑 `config.json`（面板会提示为重启项） |
| ❌ 未做 | HTTPS / 内置限流 | 设计上交给反向代理（Nginx / Caddy）。服务本身只提供明文 HTTP，公网部署**必须**置于 HTTPS 反代之后 |

## ⚙️ 本分支新增的配置项

其余配置项（账号池 / 冷却 / 超时 / 提示词 / Redis / 定时任务时点等）**含义与默认值见 ① 的 README 与 `config.example.json`**（② 未改动它们，本分支也未改动）。

| 字段 | 默认 | 说明 |
|---|---|---|
| `model_realm.prefer` | `cn` | **裸模型名的默认域**（`cn` / `global`）：只在两域都有同名模型时生效（单域模型由目录事实强制）。面板模型页「重复模型」视图里可改，保存即生效、无需重启 |
| `model_realm.pins` | `{}` | 逐模型钉死域（裸模型名 → `cn` / `global`），优先于 `prefer`；面板同视图里逐个钉。**硬指定**：该域无可用账号时不回落，宁可报错也不悄悄换域 |

## 🚀 跑起来

部署方式与 ① ② 一致（Docker / 单文件二进制 / 源码）。最短路径：

```bash
# Docker Compose（推荐服务器部署）
git clone https://github.com/OkRoromori/workbuddy2api-panel-public.git
cd workbuddy2api-panel-public
cp config.example.json config.json      # compose 挂载此文件，缺失会导致容器启动失败
docker compose up -d --build
curl -s http://localhost:7863/healthz   # 无可用账号时返回 503
```

```powershell
# Windows 单文件（本仓库不发布预编译产物，需要自建；Go 1.22+）
go build -trimpath -ldflags="-s -w" -o wb2api.exe ./cmd/server
.\wb2api.exe -config config.json        # 首启自动生成 config.json（含随机 api_key，日志打印一次）
```

然后打开 **`http://127.0.0.1:7863/panel/`**（或 **`http://localhost:7863/panel/`**）。

### 添加账号

面板右上角「**添加账号**」（② 的流程）：弹窗里先选**国内版（CN）**还是**国际版（Global）**，再打开显示的授权链接完成登录 → 凭证落盘并**热加载进池（免重启）**，顺带完成首次签到（国际服走激活 + 领 trial）。

> 授权链接不绑账号：你在浏览器里**当前登录的是哪个号，点同意出来的就是哪个号**。每个号都要在官方页面登录一次（那是官方在验身份），一个链接只能出一个号——但下一个链接会自动生成，你只需顺着点同意。

本分支在这条流程上补的是：**对已入池账号**单独触发国际服激活（账号行「激活」/ 工具栏「激活国际服」），以及修掉激活后立刻读余额为 0 的问题。

### 接入客户端

一个端口同时提供三种协议（填你自己的地址与密钥；地址里的 `127.0.0.1` 换成 `localhost` 一样能用，两者等价）：

| 客户端类型 | Base URL 填 | 实际请求的端点 |
|---|---|---|
| OpenAI 兼容（Cherry Studio / Chatbox / NextChat / 各类 SDK） | `http://127.0.0.1:7863/v1` | `POST /v1/chat/completions` |
| Anthropic 协议（Claude Code、ccswitch 等） | `http://127.0.0.1:7863` | `POST /v1/messages`（认证头用 `x-api-key`） |
| Responses 协议（Codex 等） | `http://127.0.0.1:7863/v1` | `POST /v1/responses` |

- **API Key**：`config.json` 的 `api_key`；想给不同用途分开配额，用面板「密钥」页另建
- **模型名**：填 `/v1/models` 拉到的**裸模型名**（如 `deepseek-v4-flash`）。网关没有 `auto` 之类的占位名，上游不认的模型名会原样报错（`11102`）；列表按该密钥的白名单过滤，拉不到就是这把密钥没授权
- **地址写法**：`127.0.0.1` 与 `localhost` 等价，`http://localhost:7863/v1` 同样可用。默认 `listen: ":7863"` 监听所有网卡，两种写法都通；若把 `listen` 收窄成 `127.0.0.1:7863`，个别系统上 `localhost` 会先解析到 IPv6 的 `::1`，连不上就改用 `127.0.0.1`
- **网络**：默认 `listen: ":7863"` 监听所有网卡。只给自己用就改成 `127.0.0.1:7863`；**要对外开放前必须先设 `api_key`——它为空的网关完全无鉴权**。容器里的客户端访问不到宿主机 `127.0.0.1`

## 🖥️ 管理面板

内嵌单页面板（`internal/panel`，go:embed 进二进制），地址 `http://127.0.0.1:7863/panel/`（`http://localhost:7863/panel/` 同样可用）。鉴权与 API 同口径（`api_key` 非空时输一次，存 localStorage）。顶部有**国服 / 国际服域开关**、主题切换、「添加账号」「刷新」「重启」。左侧导航十个视图（**标 🆕 的三个是本分支新增，其余 7 个来自 ②**——多数改了名，其中 ② 的「用量」页被改造成审计口径）：

| 视图 | 功能 |
|---|---|
| 仪表盘 🆕 | KPI 与消耗构成（按模型 / 按密钥）、趋势曲线、积分口径；跟随域开关切片 |
| 请求审计 🔧 | ② 的「用量」页改造而来：审计流水驱动的按日请求明细（账号 / 模型 / token / 积分），可按域与账号筛选 |
| 积分构成 | 账号对比：把每个账号的余额摊开成逐包明细（面额 / 剩余 / 已用 / 发放 / 到期） |
| 账号 | 统计条 + 账号表：状态标签、国服/国际服徽标、积分量条、成功失败计数、在途；单号操作（签到 / 余额 / 任务 / **验活 🆕** / 激活 / 解冻 / 禁用 / 移除）；批量「全部签到」「旅行巡检」「活跃上报」「全部保活」「激活国际服」 |
| 模型 | 实时查询上游目录：积分倍率、默认思考档、支持档位、上下文长度与最大输出（有探测数据时叠加实测上限与钳制告警）；「重复模型」chip 逐个钉域 |
| 密钥 🆕 | 密钥列表与用量、改名、模型白名单（支持搜索与域筛选）、单密钥并发上限、生成朋友专用连接脚本 |
| 任务管理 🆕 | 签到 / 旅行 / 活跃 / 保活的时点与开关、手动触发、每账号任务结果（国服专属） |
| 任务中心 | 全账号任务扫描 + 执行队列 |
| 运行日志 | 最近 500 行服务日志 + 请求表格日志（可开关自动滚动），按「任务 / 对话 / 保活 / 系统」分频道（keepalive 频道为本分支新增） |
| 设置 | 在线编辑 config.json |

## ❓ 常见问题（只列本分支相关）

**大请求体（多图 / 长会话）会被截断吗？**

不会。现在的规则是整体读入、上限 **64 MiB**、超限**显式 413**（`request_body_too_large`），绝不静默截断。2026-09-20 之前确实有一处真缺陷且在本仓库侧：鉴权层为读 `model` 字段按 **1 MiB** 预读并把**截断后**的内容回填 `r.Body`，长会话被砍成半截 JSON 照原样转发，上游报 `11101 unexpected EOF`、换号无效、最终以 `503` 透出——症状极像"上游限制"，但根因在我们这边（当时"上游 1 MiB 截断"的结论是误判）。实测上游对 1.4 MiB 请求体照收：修前 503、修后 200。客户端仍应控制上下文体积——超出**模型上下文**时上游以 `11115` 拒绝，那是真正的模型限制，与请求体字节数无关。

**Docker 部署登录后报「写入 auths/…json.tmp 失败： permission denied」？**

容器以 `app` 用户（uid 10001）运行，而宿主机的 `./auths`、`./data` 属主不是它。三种解法：`PUID=$(id -u) PGID=$(id -g) docker compose up -d --force-recreate`（推荐，非 root）；或 `sudo chown -R 10001:10001 ./auths ./data ./config.json`；或 compose 里 `user: "0:0"` 以 root 跑（NAS / 群晖不便 chown 时）。

**账号被 Disable 后如何恢复？**

用 `./login.sh` 重新登录覆盖凭证，重启后自动回池；或源码侧调用 `Pool.ReviveDisabled(uid)` 清除 `disabled` 状态。

> 其余问题——`429 code=6004` 模型级限流的冷却语义、系统提示词被内容策略误杀、官网「使用端」列归属、上游超时三段语义、选号权重公式等——都是**上游行为**，答案见 ① 的 [README](https://github.com/Sliverkiss/workbuddy2api) 与其代码。

## 🔒 安全与合规

- **凭据**：`./auths/workbuddy-<uid>.json` 是**明文** `accessToken` / `refreshToken`，权限 `0600`；`.gitignore` 已排除 `auths/`、`data/`、`config.json`，**永远不要提交**
- **本仓库已做过脱敏**：运维文档（交接记录、启动说明、连接脚本、`tools/`）不随本仓库发布；探活脚本里的真实服务器地址已换成占位符
- **网络**：默认无 TLS。公网部署**必须**设强随机 `api_key` 并置于 HTTPS 反代之后；`api_key` 为空 = 完全无鉴权
- **日志**：只记 uid 前 8 位 / TTFB / token 数，**不含**任何 token 或密钥明文
- **边界**：上游 CodeBuddy 属腾讯系商业产品，本项目是其**非官方**网关。仅限**本人授权账号**、本机 / 私有环境使用；不得共享、转售或违规分发；遵守其服务条款与所在地法律

## 免责声明

本项目为**二次开发（fork）**，涉及三层作者：

- **① 更上游的源头** [`Sliverkiss/workbuddy2api`](https://github.com/Sliverkiss/workbuddy2api) 由 **Sliverkiss** 创作——网关内核与整体设计；
- **② 原项目** [`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel) 由 **linguo2625469** 创作——Web 管理面板与面板侧运维层；
- **③ 本仓库** 在 ② 之上修改，增量见上文。

原始代码与设计的著作权归 ① ② 的作者所有。本项目仅供学习和研究使用。使用者需遵守 CodeBuddy 服务条款，自行承担使用风险（包括账号封禁、条款违约等）。作者不对任何因使用本项目产生的直接或间接损失负责。

## License

本项目采用 [MIT License](LICENSE) 开源协议，**继承自上游项目**。

- 允许任意使用、复制、修改、合并、发布、分发、再授权及销售
- **版权归属**：① 更上游的源头 © 2026 [Sliverkiss](https://github.com/Sliverkiss/workbuddy2api)（原始设计与网关内核）；② 原项目 © 2026 [linguo2625469](https://github.com/linguo2625469/workbuddy2api-panel)（Web 管理面板与面板侧运维层）；③ 本仓库 © 2026 [OkRoromori](https://github.com/OkRoromori/workbuddy2api-panel-public)（本分支增量）
- 再分发（源码或二进制形式）时，请保留 ① 与 ② 的 MIT 版权声明与许可声明（如在 NOTICE 或 README 中注明原始出处 `https://github.com/Sliverkiss/workbuddy2api`）
