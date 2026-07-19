# FoxRouters Changelog (this VPS)

**Service:** `foxrouters.service` · port **20130** · binary `foxrouters`  
**Repo:** `/root/nexus-workspace/foxrouters/`  
**Live version:** `const Version` in `main.go` (currently **5.11.2**)

Policy: **test (`go test -race`) before build/restart**. Secrets only via `.gateway.env` (gitignored).

---

## v1.2.0 — Anthropic Messages API + GPT-5.6 + cleanup tooling (2026-07-19)

### What changed

| Area | Before | After |
|------|--------|-------|
| API format | OpenAI-only (`/v1/chat/completions`) | + **Anthropic Messages API** (`POST /v1/messages`) — Claude Code compatible |
| Auth header | `Authorization: Bearer` only | + `x-api-key` (Anthropic standard) — both accepted |
| Model catalog | 39 models | **42 models** — added `cb/gpt-5.6-sol`, `cb/gpt-5.6-terra`, `cb/gpt-5.6-luna` |
| Key management | Grok delete + auth key delete only | + **CB key delete** (`DELETE /cb/keys/:key`) + **cleanup disabled** (`POST /cleanup/disabled?type=all\|grok\|cb`) |
| Dashboard | View-only for CB keys | **Delete button** per CB key + per Grok account + **Cleanup Disabled** button |
| Logging | `log.Printf` ad-hoc | **slog structured logging** (86 calls migrated, `LOG_LEVEL=debug\|warn\|error`) |
| Metrics | None | **Prometheus `/metrics`** — `foxrouters_requests_total`, `request_duration_seconds`, `active_keys`, `disabled_keys`, `circuit_state` |
| Version | Hardcoded `const Version = "5.11.2"` | **`-ldflags -X main.Version=<tag>`** — fallback `dev`, injected via Dockerfile + CI |
| Code structure | Flat `package main` (5,441 LOC) | **7 `internal/` packages** — `metrics`, `ratelimit`, `db`, `auth`, `upstream`, `proxy`, `handlers` |
| Tests | 22 unit tests | **38 tests** (22 unit + 16 integration) |
| Shutdown drain | `time.Sleep(500ms)` | **`sync.WaitGroup`** with 10s timeout (no log loss) |
| CB 429 handling | Permanent ban | **Cooldown 10min** (401 still permanent) |

### New endpoints

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/v1/messages` | Anthropic Messages API (Claude Code compatible) |
| `DELETE` | `/cb/keys/:key` | Delete a CodeBuddy key |
| `POST` | `/cleanup/disabled?type=all\|grok\|cb` | Bulk-remove permanently disabled keys/accounts |
| `GET` | `/metrics` | Prometheus metrics (public) |

### Claude Code integration

```bash
export ANTHROPIC_BASE_URL=http://localhost:20130
export ANTHROPIC_API_KEY=gw-xxx
claude
```

Model mapping: `claude-*` → `cb/claude-sonnet-4` (default), `*-grok` → `grok-4.5`, explicit `cb/*` / `grok-*` passthrough.

### Commits
```
106a4d1 feat: add GPT-5.6 models + Anthropic Messages API adapter
bd1975b feat: dashboard UI for delete CB key + cleanup disabled
cc715b7 feat: add CB key delete + cleanup disabled endpoints
3f37406 refactor: complete package split — main.go slim
170b91f refactor: extract internal/handlers
abaff4a refactor: extract internal/proxy
8f2ca67 refactor: extract internal/upstream
ffbe6a3 refactor: extract internal/auth
63f3a4d refactor: extract internal/db
c52e2cd refactor: extract internal/ratelimit
dfce981 refactor: extract internal/metrics
a7f5291 feat: version ldflags, slog structured logging, prometheus metrics, integration tests
706bbbf fix: P1 audit issues (CH port, gin ctx, CB load error, CB 429 cooldown, shutdown drain)
```

---

## v5.11.2 — Security hardening + admin scope split (2026-07-18)

### What changed
| Area | Before | After |
|------|--------|-------|
| Auth scope | Flat — any bearer = full admin | **Role-based** — `inference` (default) vs `admin` |
| Admin endpoints | All keys access `/api/keys`, `/accounts`, `/history`, `/cb-stats` | **AdminMiddleware** gates these — inference keys get 403 |
| Auth fail mode | Fail-open (no keys = allow all) | **Fail-closed** — reject if no keys loaded (override: `GATEWAY_AUTH_DISABLE=1`) |
| http.Server | No timeouts (Slowloris risk) | `ReadHeaderTimeout=10s`, `IdleTimeout=120s`, `MaxHeaderBytes=1MB` |
| HEAD /health | Hung (Gin HEAD→GET handler path issue) | Explicit `handleHealthMinimal()` — instant 200 |
| /v1/responses | Dead references in ratelimit + log path | Cleaned — only `/v1/chat/completions` is valid |
| CH error capture | `error_msg` + `response_body` empty on 400/503 | All non-2xx branches now set both fields |
| systemd | Root, no sandbox | `NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, `ProtectKernel*`, `RestrictAddressFamilies`, `CapabilityBoundingSet` |

### Commits
```
(this patch set)
```

### Migration notes
- Existing bootstrap keys (from `gateway-key.txt` / env) auto-assigned `role=admin` — no action needed.
- Redis-stored keys created before this version default to `admin` (backward compat in `parseGatewayKeyFromRedis`).
- New keys created via `POST /api/keys` default to `role=inference` unless `"role":"admin"` is specified.
- To create an admin key: `POST /api/keys {"name":"ops","role":"admin"}`.
- systemd hardening requires `ProtectSystem=strict` — gateway only writes to `WorkingDirectory` + Redis/CH sockets. If you add file writes outside `/root/nexus-workspace/foxrouters/`, loosen `ProtectSystem` to `full` or `true`.

---

## v5.10.0 — ClickHouse history + full body + dashboard JSON fix (2026-07-17)

### What changed
| Area | Before | After |
|------|--------|--------|
| History store | PostgreSQL JSONB (64KB body cap after v5.9) | **ClickHouse** `gateway.*` full body **unlimited** (ZSTD) |
| Credentials | Redis | Redis (**unchanged**) |
| Body policy | Cap 64KB then briefly 16MB soft-cap | **Unlimited** passthrough (`bodyString` no truncate) |
| Dashboard Request/Response JSON tabs | Often empty | Fixed — **log `id` as JSON string** (UInt64 > JS `MAX_SAFE_INTEGER`) |
| Stats `/history` | PG | CH flat aggregates; token totals summed in Go (no nested `sum`) |

### Commits
```
2b2edcd  fix: dashboard JSON tabs — string IDs + unlimited body
4e4e452  fix: CH stats — sum tokens in Go
2850887  fix: CH nested aggregate error 184
03100b2  feat: migrate history PG → ClickHouse
```

### Ops
- ClickHouse **26.x**, native **`127.0.0.1:9001`** (9000 taken), HTTP **8123**
- Env: `CLICKHOUSE_ADDR`, `CLICKHOUSE_DB`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`
- Schema auto-ensure on boot; TTL **90 days** on history tables
- Legacy PG data may remain on disk — **gateway no longer reads/writes it**

### Verified
- Full bodies ~90KB–900KB stored and returned via `/history/detail/:id`
- Compression ~**4.7×** on `request_logs` parts
- Dashboard list previews + lazy full JSON tabs after hard refresh

### Docs
- Skill `foxrouters-development` + `references/clickhouse-history-migration.md`
- `references/dashboard-history-json-tabs-uint64.md`

### Explicit non-goals (user decision)
- No further “Tier A / client-side” optimisations (context trim, model routing, reasoning defaults) — those are **client/Hermes**, not gateway.
- Gateway optimisations already shipped (v5.9 hot path) considered **enough** for now.

---

## v5.9.0 — Hot-path performance (2026-07-17)

**Commit:** `ae41b31`

| Optimisation | Detail |
|--------------|--------|
| `Len()` O(1) | Replace `len(GetAll())` on hot path |
| Re-enable off `Next()` | `reenableWorker` / `reenableCBWorker` every 1m |
| Quiet logs | No success-path spam |
| `AccountID` | Set from upstream account/key |
| Body log (then) | Cap 64KB toward PG (superseded by v5.10 unlimited CH) |
| Refresh | `singleflight` + lock-split (no mutex across HTTP) |
| SSE | Single unmarshal + line carry + buffer pool |
| Managers | `RWMutex` for readers |
| Version | Single `const Version` |

See `references/v5.9-performance-optimizations.md` in skill.

---

## v5.8.x — P0/P1 correctness (2026-07-17)

| Commit | Scope |
|--------|--------|
| `94ccb19` | Dashboard no live key inject; MaxBytesReader 10MB; unlock-before-save re-enable; graceful shutdown 15s |
| `465a549` | Auth RLock; import race; circuit no false-open on pool exhaust; health 2xx/3xx only |
| `ab57e8b` | 401 retry rebuild body; env DB secrets; (intermediate dashboard inject — later removed) |
| `972957b` | Persist Grok/CB disable + invalid_grant; gzip writer create/close once |

See `references/p0-p1-correctness-audit.md`.

---

## Architecture (current)

```
Client → Auth → RateLimit → proxyRequest
  ├── grok-* (+ aliases) → proxyGrok → cli-chat-proxy.grok.com
  └── cb/*               → proxyCodeBuddy → www.codebuddy.ai/v2
        ↓
  memory Next() O(k)
  Redis: tokens / credits / disabled / gw keys
  ClickHouse async: full request_logs (unlimited body, ZSTD)
```

| Store | Role |
|-------|------|
| **Redis** | Hot credentials & serve state |
| **ClickHouse** | Cold history + full JSON bodies |
| **PostgreSQL** | Retired for gateway history |

---

## Performance notes (observed)

- Gateway `/health` ~1–4 ms; `/history/recent` ~10 ms; chat latency = **upstream** (Grok p50 ~30s on large contexts; CB simple ~1–3s).
- Full body @ ~0.6 MB/req is fine at current traffic; **1k RPS full-body** is disk/network bound, not CH engine; chat 1k RPS dies at LLM/pool first.
- Remaining latency wins for “faster LLM feel” are mostly **client** (context size, model choice, reasoning effort) — deferred by operator.

---

## Quick ops

```bash
cd /root/nexus-workspace/foxrouters
export PATH=$PATH:/usr/local/go/bin
go test -count=1 -race ./... && go vet ./...
go build -o foxrouters . && systemctl restart foxrouters.service
curl -s http://127.0.0.1:20130/health
clickhouse-client --port 9001 -q 'SELECT count(), max(length(request_body)) FROM gateway.request_logs'
```

Dashboard: `http://<host>:20130/dashboard?key=<gw-key>` once (localStorage). **Never** re-inject live keys into HTML.
