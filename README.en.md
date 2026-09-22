<p align="center">
  <img src="https://raw.githubusercontent.com/DGZSbot/ai-icon/refs/heads/main/WorkBuddy.png" alt="WorkBuddy2API" width="120">
</p>

<h1 align="center">WorkBuddy2API Panel</h1>

<p align="center">
  <b>An enhanced fork of <a href="https://github.com/linguo2625469/workbuddy2api-panel">linguo2625469/workbuddy2api-panel</a> · Anthropic + Responses protocols · multi-key · account liveness probe · request audit</b>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.22.5-00ADD8?logo=go&logoColor=white&style=flat-square">
  <img alt="API" src="https://img.shields.io/badge/API-OpenAI_Compatible-412991?style=flat-square">
  <img alt="Deploy" src="https://img.shields.io/badge/Deploy-Single_Binary%20%7C%20Docker-2496ED?style=flat-square">
  <img alt="Transport" src="https://img.shields.io/badge/Transport-SSE%20%2F%20Streaming-0DBD8B?style=flat-square">
</p>

---

[中文](README.md) | **English**

> ⚠️ **This repository is a fork — of a fork. Three layers, and their contributions must be kept apart — above all, do not count the lower two as this branch's work.**
>
> **① Further upstream [`Sliverkiss/workbuddy2api`](https://github.com/Sliverkiss/workbuddy2api) (by Sliverkiss)** — the gateway core: account-pool scheduling, circuit breaking & cooldown, session stickiness, error classification, the prompt system, fingerprint scrubbing, scheduled tasks, OAuth login, the credit-task pipelines. It ships as **command line + scripts**. Cloning its **entire history** (464 commits, 4 branches) confirms it **never had a web UI** — the only `.js` files are the `.github/actions/ai-governance` bot, and the two `go:embed` targets are the `defaultprompt.md` / `model.json` data files. No `panel/` or `webui/` directory ever existed.
>
> **② The original project (what this repo is based on) [`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel) (by linguo2625469)** — **built the web admin panel** on top of ①'s core (`internal/panel`, 7 views, go:embed'ed into the binary), plus in-panel dual-realm OAuth account adding, the task centre, streak / back-to-school automation, live config editing, first-start config generation, background balance refresh, `httpauth` constant-time verification and CSP, model max-output probing. **This repository started from that one — the panel itself belongs to that layer, it is not an increment of this branch.**
>
> **③ This repository** — its increments over ② are [listed below](#-what-this-fork-changes).
>
> For base functionality (config fields, account-pool semantics, upstream error classification) read ①'s README; for panel usage read ②'s README.

## 📌 Provenance

| Item | Detail |
|---|---|
| ① Further upstream | [`Sliverkiss/workbuddy2api`](https://github.com/Sliverkiss/workbuddy2api) by **Sliverkiss** — the source of the gateway core, protocol adaptation, error classification, prompt system and account-pool design; **its README is the authoritative documentation for the base functionality** |
| ② The original project (what this repo is based on) | [`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel) by **linguo2625469** — built the **web admin panel and the panel-side operations layer** on top of ①; **this repository is forked from it**; base `v1.11.0-panel` (`b4245a8`), absorbed up to `b69d06e` (1.11.1-panel) |
| ③ This repository | [`OkRoromori/workbuddy2api-panel-public`](https://github.com/OkRoromori/workbuddy2api-panel-public) — an enhanced fork of ②; the increments are listed below |
| License | Inherits the MIT License; **all three copyright notices are kept** (the three Copyright lines at the top of [LICENSE](LICENSE)) |
| Attribution requirement | When redistributing (source or binary), keep ①'s and ②'s MIT copyright and license notices, and credit the original source `https://github.com/Sliverkiss/workbuddy2api` |

- If you found this project through this repository: **the foundation was built by ① and ② — please go star those two repositories.**
- For questions about **the gateway core** (how accounts are selected, how errors are classified, what a config field means), ①'s README and implementation are the authoritative explanation.
- The incremental changes in this repository are the responsibility of this repository's maintainer, not of ①'s or ②'s authors.

## 🆚 What this fork changes

**The baseline is ② — the project this repository forked — not ①.** This branch takes ②'s `v1.11.0-panel` (`b4245a8`) as its base and ports this line's own capabilities onto it, forming a single maintainable branch.

Legend:

- `🆕` = **neither ① nor ② has it** — added by this branch
- `🔧` = **② already has the same capability** — this branch rewires / extends it / adds an entry point

> **How this was checked** (2026-09-21): both ① (464 commits, tip `d1023f3`) and ② (70 commits, tip `b69d06e`) were cloned and compared file by file. ② has **139 files**: **104 are carried over unchanged** here, **35 differ substantively** (a module-path line alone does not count), and this repository adds **51 new files**. Every row below comes from that diff and can be verified against ②'s repository.

| | Capability | Description |
|---|---|---|
| 🆕 | **Anthropic + Responses protocols** | ① exposes OpenAI compatibility only, and ② does too (no `anthropic*.go` / `responses*.go` under `internal/server`). This branch adds `POST /v1/messages` (Anthropic, authenticated via `x-api-key`, ready for Claude Code / ccswitch) and `POST /v1/responses` (Codex, including streaming event translation). `httpauth` gains an `x-api-key` fallback — ② accepts `Authorization: Bearer` only |
| 🆕 | **Multi-key system + Keys page** | ① and ② both have a single `api_key` (no `api_keys` in the config). This branch: per-key usage, model whitelist (compared by **bare name**), per-key concurrency limit, and a dedicated connection script for your **other computers**; `/v1/models` is filtered by that key's whitelist; `/status` is restricted to the **owner key** (② uses plain `withAuth`, so any key can read the whole account-pool detail) |
| 🆕 | **Account liveness probe** | In ②, "probe" means **model max-output probing** (`scripts/probe_max_tokens.py --panel-out`) — there is **no account liveness probe**. This branch really sends one tiny request to see whether the account can still work; the verdict and bookkeeping are **the same code path** as the scheduled/batch probe (consecutive failures still auto-disable at the threshold, the same philosophy as `NoteSessionDead`) — no split between "the panel says it's fine" and "the scheduler killed it" |
| 🆕 | **Audit ledger / metrics aggregation + a separate dashboard view** | ① has no `internal/audit` / `internal/metrics`, and neither does ② (②'s "usage" page reads in-memory counters only). This branch adds an audit ledger (JSONL, 14-day default retention), per-day detail (account / model / tokens / credits, filterable by realm and account), per-realm credit sampling (balance / consumption / net rate / expiring batches, **persisted** across restarts), and a separate `dashboard` view (KPIs, by model, by key, trends, credit basis) |
| 🆕 | **CN / Global realm view switch** | ② has CN/Global at **login** and a `realm` skeleton (`internal/pool/realm.go`), but no "which data do I look at" switch (no `realm_filter.go`, and the UI has no 国服/国际服 wording). This branch adds a three-state top-bar switch (All ｜ CN ｜ Global, stored in the browser) that accounts / models / credit composition / audit / dashboard / the per-key usage column / the default realm when adding an account all follow. **It only affects which data you see — it does not participate in request routing.** In Global mode, CN-only pages show an explanation and make no requests |
| 🔧 | **Model realm routing: `model_realm` + bare names** | ② expresses the realm through a **model-name prefix** (`/v1/models` returns `cn:xxx`, `resolveModel` understands `cn:` / `global:`), and there is no `model_realm` in the config. This branch delivers **bare names** (clients can use whatever they pull) and resolves the realm via the new `model_realm`: explicit prefix > catalogue fact (a model only one realm has is forced to that realm) > per-model `pins` > global `prefer`. The soft default **falls back across realms** when the target realm has no usable account; hard assignments (prefix / pin) do not fall back. Pin individual models in the panel's model page under the "duplicate models" chip |
| 🔧 | **Dual-UA catalogue probing** | ② already merges **two endpoints** (`/v3/config` as the authority plus the enterprise endpoint family, concurrent with single-path degradation), but it probes `/v3/config` with a single UA. This branch found that endpoint to be **UA-sensitive**: the IDE UA and CLI UA catalogues are **complementary** (third-party model families are only delivered in the CLI catalogue, while the IDE catalogue only has internal/tool families). Probing both and merging raised the catalogue from 34 to 57 entries (`deepseek-v4.1-flash` free tier, `gpt-6-astra`, …). A single path failing still uses the other |
| 🔧 | **One-click Global activation** | ② automatically completes the global region / activation / trial claim **at login time** (`internal/panel/login.go`'s realm branch), but has no entry point to trigger it for an **account already in the pool**. This branch adds one: a per-account "Activate" button plus a toolbar "Activate Global", returning each step's result. It also fixes "0 credits read right after activation": the trial is **booked asynchronously** (measured: ~1s between the call and the credit pack's `CreateTime`), so reading the balance immediately afterwards used to return 0 |
| 🔧 | **Panel rework** | ②'s panel has 7 views (accounts / usage / packages / taskscenter / models / config / logs). This branch adds **three views** — `dashboard` / `keys` / `checkin` (10 in total) — plus content-fingerprint `ETag` + `Cache-Control: no-cache` on static assets (a plain reload shows the new build; previously it took a hard refresh), a `keepalive` log channel and **on-disk persistence** for the ring log (② has chat/task/sys only, in memory), and three fixes to the panel itself (a `.sw` class-name collision, a stray `title` on the whole account row, a copy typo) |
| 🆕 | **Path tolerance** | `internal/server/paths.go`: `/v1/v1/models`, `/models`, trailing slashes and similar variants all resolve (neither ① nor ② has this file, so one extra or missing `/v1` means a 404) |
| 🆕 | **Upgrade script** | `upgrade-run.sh` / `deploy/upgrade-run.sh`: fingerprint check → ownership check → backup (keeping only the most recent) → atomic replace → restart → smoke checks (`/healthz` `/status` `/panel/api/usage`) → prints the rollback command |
| 🆕 | **Server-side half of multi-device access** | The **connection script** generated by `internal/panel/tunnel_share.go` **embeds an Ed25519 private key** and pairs with a restricted server account (`wb2tunnel`, allowed to forward `127.0.0.1:7863` only — no command execution, no TTY). **Its purpose is to save you from deploying on every machine**: copy the script to another computer and double-click it. Regenerating rotates the key, and deleting a key revokes the tunnel authorisation |

### Detailed changes (beyond the table above)

**API surface**
- 🆕 **`/v1/models` returns bare names + a `realm` field + per-key whitelist filtering** (② returns prefixed names and does not filter by whitelist)
- 🆕 **`/status` restricted to the owner key**: a guest key gets 401
- 🔧 **`httpauth` dual auth header**: `Authorization: Bearer` plus `x-api-key` (② accepts the former only)

**Number semantics** (②'s usage page uses "credits per 1K tokens" and shares relative to the largest model)
- 🆕 Pricing moved to "**credits per million tokens**" (`per_mtoken`); the global figure is a **cumulative weighted average** (total credits ÷ total tokens), not a per-request average
- 🆕 The by-model table gained a "credits/Mtok" column; share is now **of the whole pool's total** (the same basis as the donut card)

**Logging & observability**
- 🔧 ②'s log view already has the chat / task / sys channels; this branch adds a **keepalive** channel (keep-alive and liveness traffic no longer mixed into the system channel) and **on-disk persistence** (`ring_persist.go`, surviving restarts)
- 🆕 The request-audit view and per-realm sampling (see the table above)

### Fixed

**First batch (diagnosed on 2026-09-20)** — both presented as "the client sees 503, reproduces on every retry, and switching accounts doesn't help". Both root causes were on the outbound side, and both were verified with **live A/B experiments** (same request, one variable changed).

| Bug | Root cause | Fix | A/B experiment |
|---|---|---|---|
| Upstream `11148` (`tool_call_sequence_broken`) wedged whole sessions | The Responses protocol sends **parallel tool calls** as multiple independent `function_call` items; the translation layer emitted one assistant message per item, so the outbound payload became `assistant(c1) assistant(c2) tool(c1) tool(c2)`. Upstream DeepSeek-family models require "an assistant that declares `tool_calls` must be immediately followed by its own results" and reject the rest | Added `mergeAdjacentToolCalls` as the first step of the outbound pipeline: back-to-back assistants are merged into one (`internal/upstream/tool_pairing.go`). ②'s pipeline has only `repackToolResultBlocks` + `cleanupOrphanToolCalls` — no such step | Split = 503/11148; merged = 200 (same model, same content). GLM-family models tolerate it, which is why it only surfaced on DeepSeek |
| Upstream `11101` (`Unmarshal chat params failed: unexpected EOF`) | The auth layer pre-read the body with a **1 MiB** cap to grab the `model` field, and back-filled the **truncated** content into `r.Body` — long sessions (> 1 MiB) were cut into half-JSON and forwarded as-is (this bug lived in this feature line; ②'s `handler.go` has no pre-read) | Read the whole body instead, with an explicit 413 above 64 MiB — never truncate silently (`internal/server/handler.go`) | A legitimate 1.4 MiB request: 503/11101 before, 200 after |
| Panel assets stuck in browser cache | Static assets carried no cache validator, so after a deploy the browser kept rendering the old panel | Content-fingerprint `ETag` + `Cache-Control: no-cache`: identical fingerprint → 304, changed → full body (`internal/panel/index.go`) | After the fix a reload picks up the new build immediately |

**Two conclusions worth keeping:** ① when the client reports 503, the root cause may be in **our outbound payload** rather than the upstream — isolate it with an A/B experiment first, don't blame the account pool; ② when "a long request turns into half-JSON" comes from **a size cap on our side**, rotating accounts will never help.

**Second batch (code review, 2026-09-21)** — the pitfalls that bite hardest when connecting agent clients; the same "session dies or fails silently" severity.

| Bug | Root cause | Fix |
|---|---|---|
| **Translated streams swallow upstream error frames** (Anthropic / Responses) | Both translators only read `choices`, so upstream `error` frames — how a 6004 rate limit or a content block arrives once the stream is open — were **silently dropped**: the agent got a "successful empty reply" with no content and no reason. (The direct OpenAI path passes them through, so only agent clients were affected.) | Anthropic emits an `error` event; Responses ends with `response.failed` (`status:"failed"` plus an error object). `StreamHint` gained a `WithErrorFrameObserver` side-channel observer (②'s `StreamHint` has no options slot); the forwarded bytes are unchanged |
| **Streaming marks success too early** | `NoteSuccess` + clearing the 11102 negative cache + binding the sticky session all happened *before* the first frame was read. On an upstream "200 + error frame" (e.g. a 6004 rate limit) the rate-limited account was recorded as healthy and stickiness nailed the whole session to it, so every following turn failed | Success marking and sticky binding are now deferred until the hop actually succeeded; when an error frame appears, the frame is classified (`FrameKind`) and the account is penalised accordingly |
| **Upstream timeouts rotated pointlessly** | The codebase had **no timeout detection at all** — timeouts shared the "network blip, rotate" path, so a doomed request rotated `MaxRotate` times (worst case ~half an hour with a 600s `header_timeout`), feeding consecutive-failure counts to healthy accounts along the way. (Note: the local handoff notes claimed "timeouts don't rotate" was already fixed; it was not in the code — this adds it.) | New `isUpstreamTimeout`: `net.Error.Timeout`, explicit deadlines, and "the client is still connected yet the context was cancelled" (our own idle watchdog cutting a stalled stream). On a hit it stops the loop — no rotation, no penalty — and returns a distinguishable `upstream_timeout` |
| **Responses non-streaming item order** | `function_call` items came before `message`, so a client replaying them produced `assistant(tool_call) → assistant(text) → tool(result)` — the result separated from its call, i.e. the 11148 shape, which none of the three outbound pipeline steps can repair | Item order is now **`message` first** (matching OpenAI itself), plus an outbound safety net that folds "tool call with no text + immediately following plain text" into one `assistant(text + tool_calls)` |
| Scrubbing missed the `reasoning` field | The thinking backfill mirrors a client's `reasoning_content` into `reasoning`, but scrubbing only covered `content` / `reasoning_content` / `tool_calls` — so a bare `11-128` escaped through that path (and that string alone gets the whole request blocked) | `reasoning` is scrubbed exactly like `reasoning_content` |
| `content: []` was not treated as "no text" | Some clients send an empty array instead of null, so back-to-back parallel tool calls were not merged — the 11148 shape again | Empty arrays count as empty and trigger the merge (non-empty arrays are still content; nothing is dropped) |
| Per-key concurrency slot leaked when the limit was hot-changed to 0 | The release path read the limit **at release time**, and a limit of 0 returned early — so the slot stayed occupied forever and every request to that key got 429 (until a process restart) once the limit was raised again | The release now uses the decision made **when the slot was taken**, not the current config |

Every item in this batch has tests (error frames for both protocols, seven timeout cases, item order, text folding); `go vet` / `go test ./...` / the frontend harness are all green.

### Syncing with ① (further upstream)

② already absorbed most of ①'s changes and this branch inherits them; the rounds below are the ones **this line** performed:

- **First round (fork baseline `53ee3a1` → `9a87758`, 34 commits)**: independent scheduling for the four task families, pool file split, 12153 needing consecutive hits to disable, 429 `code=6004` model-level rate-limit narrowing, 11101 no longer penalising the account, request-body 413, DeepSeek chain-of-thought, `reasoning_content` replay, Codex fingerprint scrubbing, the system-prompt system, configurable outbound UA, and more.
- **Second round (`9a87758` → `ea8b1e5`, 2026-09-14, low-level only)**: scrubbing hardening (`tool_calls.arguments` blind spot, bare `11-128` anti-probe rewrite, desktop identity sentence leak), the outbound header family aligned to the official three-part UA plus `X-IDE-*` / `X-Device-Token` / `X-Agent-Purpose`, concurrency fixes (client IP passed per request), check-in idempotency detection, stickiness liveness per model (auto-unbind when switching models after a 6004), and `requestID` support in reports.
- Deliberately not absorbed: ①'s Python script system (`scripts/task_runner.py` etc. — this branch does it with the panel plus pure API calls), `governance`/CI workflows, and the `.github/` issue-governance bot.

### Sync status against ② (the original project)

This branch is based on ②'s `v1.11.0-panel` (`b4245a8`, 2026-09-19) and **has absorbed everything ② shipped up to `v1.11.1-panel` (`b69d06e`, 2026-09-20)**:

| ②'s commit | Content | Status here |
|---|---|---|
| `08752df` (09-20) | `fix(panel)`: the run queue now merges mp-scope to-dos — fixes "the scan shows to-dos but the queue says there is nothing to run" | **Absorbed.** Both the scan and the queue now share one `mergeMPPending` implementation instead of each carrying its own copy, with a regression test in `internal/panel/taskcenter_test.go`: removing the call reproduces the original symptom ("no runnable to-dos, all accounts done") |
| `b69d06e` (09-20) | Version bump to 1.11.1-panel | Followed: `appVersion = "1.11.1-panel"` (meaning ②'s 1.11.1 fixes are all in; this branch's own additions are not reflected in that number) |

> Note: in a few places this branch implements things **differently** from ② (e.g. body size: ② removed the pre-check and lets the upstream respond naturally, while this branch reads the body whole with an explicit 64 MiB 413; model names: ② returns prefixes, this branch returns bare names). Those are forks, not lag — see the individual rows above.

### ⚠️ ② (the original project) has these — this branch merely carries them over

All of the following **are in ②** (do not count them as this branch's work in docs, introductions or reviews):

| Capability | ②'s implementation |
|---|---|
| **The web admin panel itself** | `internal/panel`, 7 views (accounts / usage / packages / task centre / models / settings / logs), go:embed'ed into the binary; light/dark themes, account-table health and credit bars, `esc()` frontend escaping, CSP + `X-Frame-Options` + `nosniff` |
| **In-panel dual-realm OAuth account add** | `internal/panel/login.go`: one button → choose **CN** or **Global** in the dialog → automatic polling to detect completion → on write it calls `Pool.Add`; global goes through region registration + activation + trial claim |
| **Task Centre view** | The `taskscenter` view + `internal/panel/taskcenter.go`: pool-wide task scan, execution queue (serial within an account, optionally concurrent across accounts), back-to-school status matrix, log channels |
| **One-click credit tasks / streak butler** | `internal/panel/tasks.go`, `internal/upstream/streak.go` (streak tier redemption + lottery), the back-to-school and campus-day loops, desktop-task fingerprint reverse engineering |
| **Live config editing** | The `config` view + `internal/livecfg/livecfg.go`: deep-merge + atomic replace, hot fields apply immediately, assembly-time fields report a restart is needed |
| **Config auto-generated on first start** | `cmd/server/config.go`'s `WriteDefault` + `main.go`'s `-config` help (generates a recommended config when absent) |
| **Background balance refresh** | `schedule.balance_refresh_enabled` / `balance_refresh_minutes` (default 5) + cooled accounts thawing automatically once their balance recovers |
| **`httpauth` and security hardening** | `internal/httpauth`: SHA-256 digest + `subtle.ConstantTimeCompare`; CSP / security response headers; panel frontend attribute escaping |
| **Log channels** | `internal/panel/ring.go`'s chat / task / sys channels and channel filtering |
| **Adoption (travel) report prerequisite fix** | In `internal/panel/autotask.go`, `ReportChatActivity` already precedes `BuddyAgreement` / `BuddyFirst` (this branch's `travel.go` is **byte-identical** to ②'s apart from the module path) |
| **Prefixed model names + the realm skeleton** | `/v1/models` returning `cn:` prefixes, `resolveModel`'s prefix parsing, `internal/pool/realm.go`'s `AvailableUIDsForModelRealm` |
| **Model max-output probing** | `scripts/probe_max_tokens.py --panel-out` + the `/panel/api/model_probes` endpoint, with measured ceilings and clamping warnings overlaid in the model page |
| **Two-endpoint catalogue merge** | `internal/upstream/global_models.go`: `/v3/config` as the main path plus the enterprise endpoint family, probed concurrently with single-path degradation |
| **`internal/usage` accounting, `internal/logfmt` log format, WAF governance, consecutive-failure downweighting, the cost ledger, client-disconnect upstream cancellation, 6004 stickiness exemption** | ②'s `internal/usage`, `internal/logfmt`, `internal/upstream/waf*`, `internal/pool/*` and so on; carried over unchanged here |

### ⚠️ ① (further upstream) has these — neither ② nor this branch does

Missing items confirmed by cloning ①'s entire history:

| ①'s capability | ①'s implementation | Status here |
|---|---|---|
| **auths directory hot-reload** | `internal/pool/watch.go` plus `StartAuthDirWatch` in `cmd/server/main.go`: drop a credential file into `auths/` and it joins the pool within 5s, no restart | **Absent** (this project only aligns the directory at startup; adding via the panel is immediate, dropping files by hand needs a restart) |
| **Temporary account suspend / resume** | `manual_disabled` as a separate state bit + `/admin/accounts/{uid}/{disable,enable,revive}` + `cmd/acct` CLI (check-in and keep-alive keep running while suspended) | **Absent** (the panel's "disable/remove" is a different semantic) |
| **Terminal status dashboard** | `cmd/stats` (in-place refreshing TUI with terminal-size handling) | Counterpart here is the web panel (built by ②) |
| **Activity-report CLI** | `cmd/activity` | Counterpart here is `scripts/probe_active.py` plus a panel button |

### Not done / TODO

| Status | Item | Notes |
|---|---|---|
| ✅ Fixed | Claiming `single`-family task rewards | The correct endpoint is the web domain `POST https://www.workbuddy.cn/activity/growth/tasks/<task_code>/claim` (task code in the path, no body, `x-client-platform: web`); the CLI domain used earlier returned a long-standing 400. Rewards are now **claimed automatically once the target is met** — measured +100 credits +5 energy credited, with idempotent repeat claims |
| ✅ Cracked | Desktop / interactive tasks | Via client-fingerprint reverse engineering (`/v2/report` three channels + evidence event payloads), 17/18 tasks complete in one click, purely over the API, verified on multiple accounts |
| ⚠️ Unsupported | The remaining task | `Expert_Philanthropy` requires a real donation (the server verifies the donation receipt when claiming; measured as not bypassable). The panel shows instructions |
| ❌ Not done | Panel-side Upstash / credential-directory config | Involves assembly-time wiring; must be edited by hand in `config.json` (the panel flags it as restart-only) |
| ❌ Not done | HTTPS / built-in rate limiting | Deliberately delegated to a reverse proxy (Nginx / Caddy). The service itself speaks plain HTTP only; a public deployment **must** sit behind an HTTPS reverse proxy |

## ⚙️ Config fields added by this branch

Every other field (account pool / cooldown / timeouts / prompts / Redis / task times …) is **documented with its meaning and default in ①'s README and `config.example.json`** (② did not change them, and neither does this branch).

| Field | Default | Description |
|---|---|---|
| `model_realm.prefer` | `cn` | **Default realm for bare model names** (`cn` / `global`): only applies when both realms have a model of that name (single-realm models are forced by the catalogue fact). Editable in the panel's "duplicate models" view; takes effect on save, no restart |
| `model_realm.pins` | `{}` | Pin a model to a realm (bare model name → `cn` / `global`), taking precedence over `prefer`; pin them one by one in the same panel view. **Hard assignment**: no fallback when that realm has no usable account — it would rather error than silently switch realms |

## 🚀 Getting it running

Deployment matches ① and ② (Docker / single binary / from source). Shortest path:

```bash
# Docker Compose (recommended for servers)
git clone https://github.com/OkRoromori/workbuddy2api-panel-public.git
cd workbuddy2api-panel-public
cp config.example.json config.json      # compose mounts this file; a missing one makes the container fail to start
docker compose up -d --build
curl -s http://localhost:7863/healthz   # returns 503 while there is no usable account
```

```powershell
# Windows single binary (this repository publishes no prebuilt artifacts, so build it; Go 1.22+)
go build -trimpath -ldflags="-s -w" -o wb2api.exe ./cmd/server
.\wb2api.exe -config config.json        # first start generates config.json (random api_key, printed once in the log)
```

Then open **`http://127.0.0.1:7863/panel/`** (or **`http://localhost:7863/panel/`**).

### Adding an account

Click "**Add account**" in the panel's top-right corner (②'s flow): pick **CN** or **Global** in the dialog, then open the authorization link it shows and complete the login → credentials are written to disk and **hot-loaded into the pool (no restart)**, with the first check-in along the way (Global accounts go through activation + trial claim).

> The authorization link is not bound to an account: **whichever account your browser is currently logged into is the one you get when you click accept.** Each account must log in on the official page once (that is the official identity check) and one link yields one account — but the next link is generated automatically, so you just keep clicking accept.

What this branch adds on top of that flow: triggering Global activation for an **account already in the pool** (per-account "Activate" / toolbar "Activate Global"), and fixing the 0-credit read right after activation.

### Client setup

One port serves three protocols at once (substitute your own address and key; `localhost` works exactly like `127.0.0.1`):

| Client type | Base URL | Endpoint actually called |
|---|---|---|
| OpenAI-compatible (Cherry Studio / Chatbox / NextChat / SDKs) | `http://127.0.0.1:7863/v1` | `POST /v1/chat/completions` |
| Anthropic protocol (Claude Code, ccswitch, …) | `http://127.0.0.1:7863` | `POST /v1/messages` (auth header is `x-api-key`) |
| Responses protocol (Codex, …) | `http://127.0.0.1:7863/v1` | `POST /v1/responses` |

- **API key**: the `api_key` from `config.json`; to give different purposes separate quotas, create extra keys on the panel's "Keys" page
- **Model name**: use the **bare model name** you get from `/v1/models` (e.g. `deepseek-v4-flash`). There is no `auto` placeholder — a model name the upstream doesn't recognise fails as-is (`11102`). The list is filtered by that key's whitelist, so a model you cannot see is one that key is not authorised for
- **Address form**: `127.0.0.1` and `localhost` are equivalent, so `http://localhost:7863/v1` works too. The default `listen: ":7863"` binds all interfaces, so both forms connect; if you narrow `listen` to `127.0.0.1:7863`, on some systems `localhost` resolves to IPv6 `::1` first — if that fails to connect, use `127.0.0.1`
- **Network**: the default `listen: ":7863"` binds all interfaces. If it is only for yourself, change it to `127.0.0.1:7863`. **Before exposing it, set an `api_key` — a gateway with an empty one has no authentication at all.** A client inside a container cannot reach the host's `127.0.0.1`

## 🖥️ Admin panel

An embedded single-page panel (`internal/panel`, embedded via go:embed) at `http://127.0.0.1:7863/panel/` (or `http://localhost:7863/panel/`). Auth follows the API (asked once when `api_key` is non-empty, remembered in localStorage). The top bar carries the **CN / Global realm switch**, the theme toggle, and "Add account" / "Refresh" / "Restart". Ten views in the left navigation (**the three marked 🆕 are added by this branch; the other 7 come from ②** — most renamed, and ②'s "usage" page reworked into the audit basis):

| View | Function |
|---|---|
| Dashboard 🆕 | KPIs and consumption breakdown (by model / by key), trend curves, credit basis; sliced by the realm switch |
| Request audit 🔧 | Reworked from ②'s "usage" page: per-day request detail driven by the audit ledger (account / model / tokens / credits), filterable by realm and account |
| Credit composition | Account comparison: each account's balance broken into per-pack detail (face value / remaining / used / issued / expiry) |
| Accounts | Stat bar + account table: status tags, CN/Global badge, credit bar, success-failure counts, in-flight; per-account actions (check-in / balance / tasks / **liveness probe 🆕** / activate / thaw / disable / remove); batch "Check in all", "Travel sweep", "Activity report", "Keep-alive all", "Activate Global" |
| Models | Live upstream catalogue: credit multiplier, default reasoning tier, supported tiers, context length and max output (with measured ceiling and clamping warnings overlaid when probe data exists); pin realms one by one under the "duplicate models" chip |
| Keys 🆕 | Key list with usage, rename, model whitelist (searchable and filterable by realm), per-key concurrency limit, generation of a connection script for your other computers |
| Task management 🆕 | Check-in / travel / activity / keep-alive times and switches, manual triggers, per-account task results (CN-only) |
| Task Centre | Pool-wide task scan plus execution queue |
| Runtime logs | The last 500 log lines + the tabular request log (auto-scroll toggleable), split into task / chat / keep-alive / system channels (the keepalive channel is new here) |
| Settings | Edit config.json in the browser |

## ❓ FAQ (this branch only)

**Will a large request body (many images / a long session) be truncated?**

No. The rule today is: read the body whole, cap at **64 MiB**, and return an **explicit 413** (`request_body_too_large`) above it — never truncate silently. Before 2026-09-20 there was a real bug and it was on our side: to read the `model` field, the auth layer pre-read the body with a **1 MiB** cap and back-filled the **truncated** content into `r.Body`, so long sessions were cut into half-JSON and forwarded as-is; the upstream reported `11101 unexpected EOF`, rotating accounts changed nothing and the gateway finally surfaced `503` — the symptoms look exactly like "an upstream limit", but the root cause was ours (the earlier "upstream truncates at 1 MiB" conclusion was a misdiagnosis). Measured: the upstream accepts a 1.4 MiB body — 503 before the fix, 200 after. Clients should still control context size: exceeding the **model's context** makes the upstream reject with `11115`, which is a genuine model limit unrelated to the body's byte size.

**Docker deployment logs in but reports "failed to write auths/…json.tmp: permission denied"?**

The container runs as the `app` user (uid 10001) while the host's `./auths` and `./data` are owned by someone else. Three ways out: `PUID=$(id -u) PGID=$(id -g) docker compose up -d --force-recreate` (recommended, non-root); or `sudo chown -R 10001:10001 ./auths ./data ./config.json`; or `user: "0:0"` in compose to run as root (for NAS / Synology where chown is awkward).

**How do I recover a disabled account?**

Log in again with `./login.sh` to overwrite the credentials; it rejoins the pool after the restart. Or call `Pool.ReviveDisabled(uid)` in code to clear the `disabled` state.

> Everything else — `429 code=6004` model-level rate-limit cooldown semantics, system prompts killed by the content policy, the official site's "client" column attribution, the three upstream timeout phases, the account-selection weight formula — is **upstream behaviour**; see ①'s [README](https://github.com/Sliverkiss/workbuddy2api) and its code.

## 🔒 Security & compliance

- **Credentials**: `./auths/workbuddy-<uid>.json` holds **plaintext** `accessToken` / `refreshToken` at `0600`; `.gitignore` already excludes `auths/`, `data/` and `config.json` — **never commit them**
- **This repository is already sanitised**: operations documents (handoff notes, start-up notes, connection scripts, `tools/`) are not published here, and the real server address in the probe script has been replaced with a placeholder
- **Network**: no TLS by default. A public deployment **must** set a strong random `api_key` and sit behind an HTTPS reverse proxy; an empty `api_key` means no authentication at all
- **Logs**: only the first 8 characters of the uid / TTFB / token counts — **no** token or key plaintext
- **Boundary**: CodeBuddy is a commercial Tencent product and this project is its **unofficial** gateway. Use it only with **accounts you are authorised to use**, in a local / private environment; do not share, resell or redistribute in violation of its terms; comply with its terms of service and your local laws

## Disclaimer

This project is a **derivative work (fork)** involving three layers of authorship:

- **① The original project** [`Sliverkiss/workbuddy2api`](https://github.com/Sliverkiss/workbuddy2api) was created by **Sliverkiss** — the gateway core and overall design;
- **② The original project** [`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel) was created by **linguo2625469** — the web admin panel and the panel-side operations layer;
- **③ This repository** modifies ②, with the increments listed above.

Copyright in the original code and design belongs to the authors of ① and ②. This project is for learning and research only. Users must comply with the CodeBuddy terms of service and bear the risks of use themselves (including account bans and terms violations). The author accepts no liability for any direct or indirect loss arising from the use of this project.

## License

This project is released under the [MIT License](LICENSE), **inherited from the upstream project**.

- Use, copy, modify, merge, publish, distribute, sublicense and sell freely
- **Copyright**: ① original project © 2026 [Sliverkiss](https://github.com/Sliverkiss/workbuddy2api) (original design and gateway core); ② original project © 2026 [linguo2625469](https://github.com/linguo2625469/workbuddy2api-panel) (web admin panel and panel-side operations layer); ③ this repository © 2026 [OkRoromori](https://github.com/OkRoromori/workbuddy2api-panel-public) (this branch's increments)
- When redistributing (source or binary form), keep ①'s and ②'s MIT copyright and license notices (e.g. credit the original source `https://github.com/Sliverkiss/workbuddy2api` in a NOTICE or the README)
