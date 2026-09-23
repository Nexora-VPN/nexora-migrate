# Migrating an existing panel into Nexora — how, and why that way

Status: **built 2026-09-14.** The tool in this repository implements what
follows; `README.md` is the operator's view and this is the reasoning. This
document
settles the two questions that decide every line of code that follows: *how do
we get the data out of the old panel*, and *how do we get it into Nexora*.
Everything in the "verified" tables below was read out of the upstream
repositories on 2026-09-14, not remembered.

## Rulings (2026-09-14) — do not re-propose the opposite

1. **Acquisition: database/backup file primary, source REST API secondary.** §2.
2. **No panel-side work.** `nexora-migrate` uses only the panel APIs that exist
   today. No `POST /api/users/import`, and no legacy subscription
   compatibility routes. The consequences are written into §4 and §5.1 — the
   loader works around both, it does not wait for them.
3. **Build order: s-ui → 3x-ui → Marzban → Hiddify → PasarGuard → Remnawave →
   Marzneshin**, and the classic **x-ui** line added after them on 2026-09-14.
4. **Public repository**, like `nexora-openvpn`. It holds no panel secrets and
   is read by exactly the operators we are trying to win.
5. **A local web UI from the first release**, not a later addition — §6b, and
   it is what shipped: `nexora-migrate` with no arguments binds 127.0.0.1,
   prints a one-time link, serves a five-step wizard and closes itself. There
   is no separate CLI: the wizard *is* the interface, and an operator who has
   to run it where the data is tunnels to it (`ssh -L`) rather than typing
   flags.
6. **File input is SQLite only; every other panel is read over its API.** No
   `mysqldump` parsing, no MySQL or Postgres drivers — which is also what keeps
   the build to one cgo-free command on every platform, Windows included.
7. **The scope is everything, not just accounts.** Inbounds, outbounds,
   endpoints, routing, DNS and admins are converted and offered alongside
   clients, in a nested tree the operator ticks. §5.3 argued for users-only;
   that was overruled, and the answer to its objection is that a converted
   inbound arrives **listed, explained and unticked-able where it is wrong**
   rather than silently plausible.

---

## 0. What migration actually means here

`nexora-panel/docs/product.md` records it plainly: **"The biggest remaining
migration blocker is Marzban / 3X-UI import. Export shipped; import was
deliberately dropped and is still the thing that keeps an existing operator on
their current panel."**

So the goal is not a generic ETL. It is one sentence:

> An operator with 4,000 paying customers on Marzban or 3x-ui can move to
> Nexora **without their customers noticing** — same credentials, same
> subscription links, same remaining quota and expiry date.

Three consequences follow from "without their customers noticing", and they are
what the design has to optimise for:

1. **Credentials must survive byte-for-byte.** A regenerated uuid is a support
   ticket per customer.
2. **Subscription URLs should survive** where the URL shape allows it (§5.1).
   This is worth more than every other feature in the tool combined: it is the
   difference between "repoint DNS" and "message 4,000 people".
3. **Quota and expiry must survive**, including the half-consumed state. An
   account that silently gets its 300 GB back, or loses 20 remaining days, is
   worse than no migration.

Explicitly **not** goals: importing the source panel's inbounds, hosts, routing
or nodes (§5.3), or importing its traffic history.

---

## 1. The six sources, verified

Read from upstream on 2026-09-14 via the GitHub API.

| Panel | Stack / store | Users live in | Credentials shape | Channels available |
|---|---|---|---|---|
| **Marzban** (Gozargah) | Python/FastAPI + SQLAlchemy; SQLite `/var/lib/marzban/db.sqlite3` or MySQL/MariaDB | `users` + one `proxies` row **per protocol** (`settings` JSON) + `inbounds` m2m | per-protocol: vmess `{id}`, vless `{id,flow}`, trojan `{password}`, ss `{password,method}` | REST (`/api/admin/token`, OAuth2 password → JWT; `/api/users` paginated) **and** DB |
| **PasarGuard** (active Marzban fork, 2.5k★) | same lineage, `app/db/crud/` gained `api_key`, `group`, `client_template` | same shape, plus groups | same | REST with **real API keys** and DB |
| **3x-ui** (MHSanaei, 46k★) | Go + GORM, SQLite `/etc/x-ui/x-ui.db`. v3 is a large rewrite (nodes, `internal/`, API tokens) | `inbounds.settings` JSON `clients[]` **plus** `client_traffics` rows keyed by `email` | per-inbound client object: `id`/`password`, `flow`, `email`, `subId`, `tgId`, `limitIp`, `totalGB`, `expiryTime` | SQLite file **and** REST (v2: session cookie only; v3: `ApiToken` with scopes `admin`/`monitor`/`node-sync`) |
| **s-ui** (alireza0, 9.9k★) | Go + GORM + sing-box, SQLite `/usr/local/s-ui/db/s-ui.db` | `clients` table | one `config` JSON blob per client | SQLite file **and** REST (`tokens` table) |
| **Hiddify Manager** (9.2k★) | Python/Flask, MySQL (or SQLite) | `user` table (+ `user_detail` per child) | **one uuid per user**, plus optional `wg_pk`/`ed25519_*` | REST `/{proxy_path}/api/v2/admin/user/` with `Hiddify-API-Key`, **and** DB |
| **Marzneshin** (699★, last push 2025-10 — effectively dormant) | Python/FastAPI, Postgres/MySQL/SQLite | `users` (+ `services` m2m) | single `settings` JSON string; `key` column is the sub secret | REST (`/api/admins/token`) and DB |
| **Remnawave** (5k★) | TypeScript/NestJS + **Postgres only**, Prisma | Prisma models | per-user | REST (JWT / API keys); Postgres otherwise |

Two observations that drive everything:

- **For 3x-ui, s-ui and single-file Hiddify/Marzban installs, "the backup file"
  *is* the database.** `x-ui.db` and `s-ui.db` are plain SQLite files; that is
  exactly what the 3x-ui Telegram-bot backup ships. There is no separate backup
  format to parse. A reader that speaks SQLite therefore covers the
  backup-file case and the live-DB case with the same code.
- **s-ui's `clients` table is almost Nexora's `users` table.** `Volume`,
  `Expiry`, `Up`, `Down`, `Desc`, `Group`, `Remark`, `AutoReset`, `ResetDays`,
  `NextReset`, `TotalUp`, `TotalDown`, `OnlineAt`, `DelayStart` — same names,
  same meanings. s-ui is the cheapest source to implement and should be the
  reference implementation the other readers are written against.

---

## 2. Decision A — how the data is acquired

### The four candidate channels

**C1. Live REST API of the source panel.**

- *For:* no DB drivers and no file handling; survives storage changes; returns
  fields the DB does not store (Marzban's `subscription_url` and `links` are
  computed, not columns); respects the source's own permissions, so a reseller
  can migrate only their own book; works when the DB is inside a container the
  operator can't reach.
- *Against:* the old panel must be **up, reachable and healthy** — often it
  isn't, which is why they're leaving; admin credentials must be handed to a
  tool; 20k users is thousands of paginated requests against a production
  panel; Cloudflare, a custom proxy path or 2FA in front of it all break it;
  3x-ui ≤ v2 has no token auth at all (session cookie scraping); and some
  things are simply not exposed — **Marzban's JWT secret is not**, per-node
  usage is not, admin ownership sometimes is not.

**C2. Direct database connection** (SQLite path, or MySQL/Postgres DSN,
optionally through an SSH tunnel).

- *For:* **complete** — everything the panel knows, including the secret key,
  creation times, admin ownership and per-node usage. One query per table
  instead of 200 paginated calls. Unaffected by API versioning. Works when the
  panel is stopped, broken, or already uninstalled. All three drivers exist in
  pure Go, so Nexora's CGO-free rule survives.
- *Against:* schema churn across source versions (mitigable — read defensively:
  probe `PRAGMA table_info` / `information_schema.columns` and select only
  columns that exist); needs reachability to the DB or a tunnel; reading a live
  DB mid-write can tear (mitigable — SQLite `immutable=1`/`mode=ro` on a copy,
  or a transaction snapshot).

**C3. A backup artifact the operator hands over** (the `.db` file itself, a
`mysqldump`, a `/var/lib/marzban` tarball).

- *For:* the safest channel there is. Read-only, offline, no credentials in
  flight, **reproducible** — the same file always produces the same result,
  which is the only thing that makes "dry-run, then apply" an honest promise.
  Attachable to a support ticket. Works when the source server is already gone.
- *Against:* someone has to produce it. And there is one real trap: **a MySQL
  `.sql` dump needs either a full SQL parser or a throwaway MySQL to restore
  into.** Do not write the parser. Accept SQLite files natively; for MySQL
  installs ask for a DSN or a tunnel (C2) instead, and accept a `.sql` only via
  an optional "restore into a temporary database yourself, then point me at
  it" path.

**C4. Operator-supplied CSV / JSON.**

- *For:* the escape hatch — panels we will never support, hand-kept
  spreadsheets, a reseller's own book. Costs almost nothing because it is the
  intermediate format (§3) with a simpler spelling.
- *Against:* no credential fidelity unless the operator has the uuids.

### Recommendation

> **C2 and C3 are one code path and are the primary channel; C1 is a
> per-source convenience; C4 is the escape hatch.**

The reasoning, in one line: *the database is the only channel that contains
everything, and for most sources the backup file **is** the database — so one
reader serves both, and the API reader is there for the operator who cannot or
will not copy a file.*

But the **default differs per source**, and pretending otherwise is how this
kind of tool goes wrong:

| Source | Recommended primary | Why |
|---|---|---|
| 3x-ui | SQLite file | one file, no auth story, v2 has no tokens; `client_traffics` + `inbounds.settings` is the whole picture |
| s-ui | SQLite file | same, and the model is near-identical to ours |
| Marzban | **REST API**, DB as fallback | `/api/users` returns `subscription_url`, proxies and `used_traffic` in one call; the DB is MySQL on any real install and needs a 3-way join over `users`/`proxies`/`inbounds` |
| PasarGuard | REST API | real API keys; same shape as Marzban |
| Hiddify | REST v2, or MySQL | one uuid per user — a tiny mapping either way |
| Marzneshin | either, low priority | dormant upstream, small install base |
| Remnawave | REST API | Postgres + Prisma; the API is the only sane surface |

Both readers sit behind one interface, so a source can gain the second channel
later without touching anything downstream:

```go
type Source interface {
    Probe(ctx) (Fingerprint, error)   // panel name + version + row counts
    Extract(ctx, chan<- Record) error
}
```

---

## 3. Decision B — what flows through the middle

### Option 1: direct pipe (source reader → Nexora API, in one process)

Fewer moving parts, one command, no format to document. But: the extract cannot
be reviewed before it is applied; a failed run halfway through has nothing to
resume from; and every source reader needs a live source panel to test against,
which means CI cannot test any of them.

### Option 2: a versioned intermediate bundle — **recommended**

`extract` writes a `bundle.zip`; `plan`/`apply` read it.

```
bundle.zip
  manifest.json     source panel, version, extracted_at, row counts, tool version
  users.ndjson      one record per line
  admins.ndjson     optional
  mapping.yaml      generated skeleton: source inbound/service/group → Nexora template + group
  notes.md          everything the reader could not represent (human-readable)
```

Why this is worth the extra format:

- **N sources × 1 target instead of N × M glue.** A new panel is a new reader,
  not a new migration.
- **Dry-run becomes real.** Extract on the old server, review the bundle and
  the report, edit `mapping.yaml`, load from anywhere. The two panels never
  need to be online at the same moment.
- **CI can test every reader** against a checked-in 30-row fixture database per
  source *per version*. This is the single thing that keeps the tool alive as
  upstream churns — and 3x-ui's v2→v3 rewrite shows how fast that churn is.
- The operator can fix things in the bundle with `jq` — drop test accounts,
  resolve a name collision — without us building a UI for it.
- It doubles as the Nexora→Nexora transfer format, and as the format a future
  in-panel importer or migration addon would accept.

The one risk is inventing a third data model. **Avoid it by construction:** the
bundle record is deliberately *the `POST /api/users` body we intend to send*
plus a small `source` envelope. It is the plan, written down — not a schema.

```jsonc
{
  "source": { "panel": "marzban", "id": "1841", "name": "ali_1401",
              "sub_token": "YWxpXzE0MDEsMTcM...", "inbounds": ["VLESS_TCP_REALITY"],
              "admin": "reseller2" },
  "user":   { "name": "ali_1401", "enable": true, "volume": 107374182400,
              "expiry": 1772323200, "up": 0, "down": 41203847362,
              "config": { "uuid": "…", "flow": "xtls-rprx-vision", "password": "…" },
              "subId": "YWxpXzE0MDEsMTcM…", "group": "reseller2",
              "templateIds": [3] },
  "warnings": ["vmess uuid differs from vless uuid; vmess configs will change"]
}
```

---

## 4. Decision C — how it is written into Nexora

### Option 1: direct writes into the Nexora database — **reject**

Fast, and wrong. It bypasses subscription-token generation, template
many-to-many membership, the live node reconciliation
(`SyncNodeLive`), the audit log, licence counting and reseller ownership; it
requires the panel to be stopped; and it breaks on every panel schema change.
The one arguable use — seeding a brand-new empty panel offline — is not worth a
second write path.

### Option 2: through the panel's REST API — **recommended**

Everything the panel owns happens: `SubToken` generation, template membership,
one node push per node, the audit trail, licence caps, reseller ownership, name
validation. It needs an API token, not database credentials, and works against
a remote panel over TLS.

The cost is throughput, and this is the sharp edge of ruling 2. There is no
bulk *create*: `userBulkOps` in `internal/api/userbulk.go` carries only
`enable`, `disable`, `reset-traffic`, `delete`, `reset-devices`, `add-days`,
`add-traffic`. So 20,000 accounts are 20,000 `POST /api/users` calls, and each
one ends in `gw.ReconcileUser` → `SyncNodeUsers` **per node the account lands
on** (`internal/gateway/reconcile.go:116`). Three nodes turns that into 60,000
full user-list syncs.

The module API (`POST /api/module/users`, bearer token of kind `module_api`) is
not a way out: it forces `AllTemplates = true` and has no template-membership
surface, so it cannot express the mapping the import is built around.

**The workaround, using only routes that exist today** — and it collapses
60,000 syncs into one per node:

1. `GET /api/license` — headroom pre-flight; refuse before writing anything.
2. Take the nodes out of the loop for the duration of the load. Either import
   **before any node is attached** (the natural order on a fresh install:
   install → inbounds → templates → import → attach nodes), or
   `POST /api/nodes/{id}/enabled {"enabled": false}` for each.
3. `POST /api/users` per record, with bounded concurrency (start at 4),
   exponential backoff on 5xx, and a resumable state file.
4. Re-enable the nodes, then `POST /api/nodes/{id}/sync` **once per node**.

Step 2 is the whole trick, and it must be in the tool's own preflight output —
an operator who skips it gets a correct import and a very unhappy fleet.

Idempotency key: `(source panel, source id)`, held in the run state file, with
`GET /api/users?search=` as the existence check before a create. A re-run
updates rather than duplicates, which is what makes `--resume` and a second
corrective pass safe.

---

## 5. The parts that will actually hurt

### 5.1 Subscription-link continuity — the highest-value item

Verified in the panel today: `internal/api/subpage.go:301` resolves a
subscription with

```go
s.db.Where("sub_id = ? OR sub_token = ?", token, token)
```

— no format constraint on `sub_id`. So **carrying the source panel's token into
`User.SubId` works right now, with no panel change**, for every source whose
subscription URL is a single path segment:

| Source | Old URL | Carry? |
|---|---|---|
| Marzban | `/sub/{token}` — `b64url(username,created_at)` + 10 sig chars, verified in `app/utils/jwt.py` | ✅ into `SubId` verbatim |
| 3x-ui | `/sub/{subId}` | ✅ `subId` → `SubId` |
| s-ui | sub id **is** the client name | ✅ name → `Name` and `SubId` |
| PasarGuard | Marzban-shaped | ✅ |
| Marzneshin | `/sub/{username}/{key}` — **two segments** | ❌ collides with our `/sub/{token}/{format}` route |
| Hiddify | `/{proxy_path}/{uuid}/sub/` — different shape entirely | ❌ |
| Remnawave | `/api/sub/{shortUuid}` | ⚠️ needs a prefix route |

For the ✅ rows, the whole migration is: import, then repoint the old hostname
at Nexora. Customers change nothing.

The ❌ and ⚠️ rows would each need a legacy compatibility route in the panel.
**Ruling 2 says no panel work, so those three sources accept that subscription
links change** — the import still carries credentials, quota and expiry, and
the operator has to re-deliver links to those customers. The tool must say so
loudly in `plan`, per source, with the affected count, rather than letting an
operator discover it after the cutover.

That is a defensible trade: Marzneshin is dormant, Remnawave's install base is
small, and Hiddify's URL carries a per-install `proxy_path` that a compat route
could not reproduce without configuration anyway. The three sources where link
continuity actually decides the sale — Marzban, 3x-ui, s-ui — all work today
with no panel change at all.

Note also that Marzban's token is *derivable* (`username,created_at` + first 10
chars of `b64url(sha256(data + JWT_SECRET))`), and the JWT secret is in the
database but **not** exposed over the API. Another point for the DB channel —
though in practice the API returns the finished `subscription_url` anyway.

### 5.2 Credential collapse

Nexora stores **one** credential set per user — `internal/api/sub.go:582`
reads exactly `{uuid, password, flow, method}` — and each protocol picks the
key it needs. Marzban, 3x-ui and Marzneshin store credentials **per protocol**.

So the collision set is narrow and exactly enumerable:

- `vmess.id` ≠ `vless.id` → one of the two protocols' configs changes.
- `trojan.password` ≠ `shadowsocks.password` → same.
- Everything else (flow, ss method) coexists.

Proposed default: uuid from vless → vmess; password from trojan → shadowsocks;
and **report every user where a discarded value differed, naming which of their
protocols will break.** Never silently.

### 5.3 Inbounds, hosts and nodes are not importable

Marzban hosts and 3x-ui inbounds are xray-shaped and carry the *source
server's* ports, domains and certificates. Nexora's inbounds are sing-box-shaped
pool items attached to templates on specific nodes. Auto-converting them
produces configs that look right and do not work.

So: **users only.** The operator builds inbounds and templates in Nexora first,
and `mapping.yaml` maps source inbound / service / group names onto Nexora
template ids and a group label. The skeleton is generated from the extract, so
it is fill-in-the-blanks; `apply` refuses to run with unmapped keys unless
`--default-template` is given.

### 5.4 Lifetime and quota semantics

Nexora has both shapes — absolute `Expiry`, and `Duration` + `ActivatedAt` for
start-on-first-use — so coverage is good, but each source spells it
differently and the mapping must be written down per source: Marzban's
`on_hold_expire_duration`/`on_hold_timeout`, Marzneshin's `expire_strategy` +
`usage_duration` + `activation_deadline`, Hiddify's `package_days` with a null
`start_date`, 3x-ui/s-ui's `delayStart`.

Traffic: 3x-ui and s-ui split up/down and carry over directly. Marzban
(`used_traffic`) and Hiddify (`current_usage`) have a single counter — it goes
into `Down`, and the report says so.

Watch the **zero-means-unlimited** trap throughout: `0` means unlimited in
`Volume`, `Expiry`, `SpeedLimit`, `IPLimit` and `DeviceLimit`. A source that
spells unlimited as `-1` (Marzneshin's `ip_limit`) or `null` (Marzban's
`data_limit`) must be normalised deliberately, not by a zero-value default.

### 5.5 Pre-flight: the licence cap

Nexora's licence caps user count. Importing 12,000 accounts into a 1,000-user
licence must fail **before** anything is written. `plan` calls `/api/license`
and reports the headroom.

### 5.6 Name collisions and charset

Marzban usernames are `[a-zA-Z0-9_]{3,32}`; a 3x-ui client "email" is free text
and is routinely Persian, with spaces. Nexora's `Name` is unique and appears in
generated link remarks. Needs a normalise step plus an explicit collision
policy (suffix / skip / fail), surfaced in the preview rather than chosen for
the operator.

### 5.7 Admins and resellers — optional, default off

Source admins can become Nexora `Admin` rows with role `resale`, and
`User.AdminID` preserves who owns whose book. Passwords cannot carry (different
hashes), so: create with generated passwords printed once. Off by default
because it is the one part of an import that can lock a sudo out.

---

## 6. Proposed shape of the tool

```
nexora-migrate extract --from 3x-ui --db /path/to/x-ui.db        -o bundle.zip
nexora-migrate extract --from marzban --api https://old --user … -o bundle.zip
nexora-migrate inspect bundle.zip                  # counts, conflicts, mapping skeleton
nexora-migrate plan    bundle.zip --panel … --token … --map mapping.yaml
nexora-migrate apply   bundle.zip --panel … --token … --map mapping.yaml [--resume]
nexora-migrate rollback --state run-2026-09-14.json
nexora-migrate ui                                  # the same five steps in a browser (§6b)
```

`plan` writes nothing and prints the full report; `apply` is chunked and
resumable from a state file and emits `result.csv`
(source name → Nexora id → status). `rollback` deletes exactly what its run
created — nothing else.

Layout (new folder, Go 1.26, CGO-free, module `github.com/nexora-vpn/nexora-migrate`):

```
cmd/nexora-migrate/     CLI and `ui`
internal/webui/         the wizard server + go:embed of frontend/dist
frontend/               Vue 3 + PrimeVue, same stack as nexora-panel/frontend
internal/bundle/        the intermediate format + validation
internal/source/        marzban/ threexui/ sui/ hiddify/ marzneshin/ remnawave/ csv/
internal/target/        Nexora API client, generated from nexora-panel/internal/api/openapi.yaml
internal/mapping/       mapping.yaml, policies, collision rules
internal/report/        human report + machine JSON
testdata/               fixture databases, per source per version
docs/
```

Suggested build order — reference implementation first, then the two that
actually matter commercially:

1. **s-ui** (SQLite) — near-1:1, proves the whole pipeline end to end cheaply.
2. **3x-ui** (SQLite) — the largest install base by far.
3. **Marzban** (API + DB) — the migration blocker named in `product.md`.
4. Hiddify → PasarGuard → Remnawave → Marzneshin (dormant, last).
5. CSV escape hatch, any time.

---

## 6b. Decision D — CLI, or a web panel of its own?

Ruling 2 removes the third possibility (a page inside nexora-panel), so the
real choice is between a terminal tool and `nexora-migrate` serving a web UI of
its own.

**Where the tool has to run decides half of this.** The primary channel is the
source panel's database, and for 3x-ui, s-ui and single-file installs that
database is a file on the old VPS. So `extract` runs over SSH on a headless
server, every time. A web UI cannot be the only interface without forcing an
`ssh -L` tunnel first — which is a terminal step, so the CLI exists either way.

**Where a GUI would genuinely pay** is the middle: reviewing four thousand rows,
seeing the collisions and the credential conflicts, and picking which Nexora
template each source inbound maps to from a dropdown populated with the target
panel's real templates. That is a table and a form, and a terminal is bad at
both.

| Option | For | Against |
|---|---|---|
| **O1. CLI only** | one static binary; runs where the data is; scriptable; no port, no auth, no CSRF, no frontend build; the whole tool is testable | reviewing 4,000 rows and hand-editing `mapping.yaml` in `vi` is genuinely unpleasant for the audience we are courting |
| **O2. CLI + local web UI** (`nexora-migrate ui`, bound to 127.0.0.1) | the wizard the operator wants: connect → preview → map with dropdowns → apply with a progress bar | a second product surface — frontend build, embed, auth, updates; on a headless VPS it still needs an SSH tunnel; and bound to `0.0.0.0` by a hurried operator it is an **unauthenticated endpoint holding the admin credentials of two panels and every subscription token** |
| **O3. CLI + a self-contained HTML report** (no server) | `plan` writes `report.html`: the full sortable table, every conflict, the counts, the generated `mapping.yaml` ready to copy. One Go template, zero attack surface, opens with a double-click after `scp` | review is read-only; mapping is still edited as YAML |
| **O4. A page in nexora-panel** | nothing to install | excluded by ruling 2, and by `product.md` decision 5, which dropped Marzban import from the users page |

### Ruling: **O2 — CLI plus a local web UI, from the first release**

Chosen 2026-09-14 over the O1+O3 recommendation above, on the argument that the
audience will not run a migration they cannot see, and that the wizard is
itself a sales argument. The objections in the table do not disappear; they
become requirements.

**The CLI does not go away.** `extract` has to run where the source database
is, which is a headless VPS reached over SSH, so every command stays available
headless and scriptable. The UI is a second front end over the same library —
never a second implementation.

**Security posture, non-negotiable**, because this process holds the admin
credentials of two panels and the entire subscription-token book:

- `nexora-migrate ui` binds **127.0.0.1** by default and prints a URL carrying a
  one-time random token; the token is exchanged for a session cookie on first
  load. No token, no access — never an open port with an implicit session.
- Any other bind address requires an explicit `--allow-remote` **and** TLS
  (`--tls-cert`/`--tls-key`); the tool refuses to start otherwise rather than
  warning and continuing. Remote operators are pointed at `ssh -L` first.
- Source and target credentials live in process memory for the run. They are
  written to the run state file only when the operator asks for `--resume`
  support, and then the file is `0600` and the UI says where it is.
- `bundle.zip` contains every subscription token in the book: `0600`, and the
  download button says so.
- The server exits when the run finishes and the window closes — it is a
  wizard, not a daemon.

**Shape of the UI** — one wizard, five steps, each mapping onto a library call
that the CLI also exposes:

1. **Source** — pick the panel, then a file picker (`x-ui.db`, `s-ui.db`) or a
   DSN/API form. `Probe` shows the detected panel version and row counts before
   anything else happens.
2. **Extract** — progress, then the bundle summary.
3. **Map** — the step that justifies the whole UI: source inbound/service/group
   on the left, a dropdown of the **target panel's real templates** on the
   right, fetched live from `GET /api/templates`. Plus the collision and
   credential-conflict policies as radio buttons.
4. **Plan** — the full table with filters and a per-row reason column, the
   licence-headroom check, and the loud warning about disabling nodes for the
   load (§4). Nothing is written.
5. **Apply** — a progress bar over server-sent events (the panel already
   streams logs this way), then `result.csv` and a persisted `report.html`.

**Stack:** Vue 3 + PrimeVue embedded with `go:embed`, matching
`nexora-panel/frontend`, so components, styling and the developer flow are the
ones this codebase already has. The target-panel client is generated from
`nexora-panel/internal/api/openapi.yaml`, same as the panel's own
`schema.d.ts`.

**The cost is real and should be planned for, not discovered:** the UI is
roughly the effort of two source readers. The build order in §6 therefore holds
— s-ui first — so the wizard is built against a reader that already works end
to end rather than in parallel with one.

---

## 7. What was built

The tool is one Go binary with no cgo and no frontend build. `go build .` is the
whole build on every platform; a Windows binary is the same command with two
environment variables, which is why the SQLite driver is `modernc.org/sqlite`
and the wizard's page is plain HTML, CSS and JavaScript behind `go:embed`.

```
main.go                    flags, and the blank imports that register readers
internal/bundle/           the canonical item + the nested selection tree
internal/normalize/        name cleaning, collisions, and saying so
internal/convert/          Xray → sing-box, share-link parsing, the client shape
internal/sqlitex/          read-only, immutable SQLite with per-column probing
internal/httpx/            JSON HTTP that quotes the server in its errors
internal/source/           one package per panel: sui, xui (3x-ui),
                           xuiclassic (vaxilu's x-ui and its forks),
                           marzban (+pasarguard), hiddify, marzneshin, remnawave
internal/source/xraysql/   what the two x-ui lines share: the account merge,
                           the inbound loop, admins, clients, rule sets
internal/source/dbfetch/   the API channel for the three file-backed panels:
                           log in, download the panel's own backup, read it,
                           delete it
internal/target/           the Nexora client: login, preflight, create
internal/apply/            ordering, id late-binding, node pause/resume
internal/web/              the wizard: server, guards, upload, SSE, assets/
```

**The bundle is the plan, written down.** Every `bundle.Item` carries the exact
JSON body that will be POSTed, built at read time and reviewable before anything
is written — so §3's intermediate format exists, but as a live object rather
than a file, because the wizard holds both halves of the migration in one
process. `IDRefs`/`IDRef` late-bind the ids that only exist after a dependency
has been created, which is how a template names its inbounds and a client names
its template.

**Two things the readers do that were not in the original design.** Marzneshin
stores no credentials at all — it derives them from the account key with a hash
whose algorithm is an install setting — so that reader fetches each account's
own published subscription and parses the credentials out of the links. It is
exact by construction and needs nobody's key derivation reimplemented. And 3x-ui
keeps one person once per inbound they are on, so its reader merges every
appearance of an email across every inbound before joining the `client_traffics`
counters on.

**The file-backed panels grew an API channel, and it is the same reader.**
s-ui, 3x-ui and the classic x-ui line all expose the endpoint behind their own
backup button, so `dbfetch` signs in, downloads the database, hands the path to
the reader that already existed, and deletes the copy. §2's ruling — file first,
API second — is what this implements rather than contradicts: the backup *is*
the panel's own serialisation of its state, complete by definition and identical
to the file channel byte for byte. Reading the same panel a second way through
its REST endpoints would be two implementations per panel that have to agree,
and the one exercised less would be the one that is wrong. The cost is that
vaxilu's original x-ui has no such endpoint at all; it is told to use the file,
by name.

**Which credentials a panel accepts is in its descriptor, not in the form.**
`Descriptor.Auth` lists the modes — login, token, or both — and the wizard shows
a picker only where there is a choice. It also sends *only* the chosen mode's
fields, so a token left in a field the operator switched away from cannot
silently beat the password they just typed, and `source.Read` refuses a
credential the panel never had (the classic x-ui line has no API tokens;
Hiddify has nothing but).

**The node-pause protocol of §4 is implemented and is the default.** The applier
disables every enabled node, runs, then re-enables and syncs each one — in a
`defer`, so an operator's fleet comes back even when the run fails or is
cancelled.

### Tested

`go test ./...` covers: the s-ui and 3x-ui readers against fixture databases
built in the test (so the schema each reader expects is visible, and an upstream
drift fails here rather than on somebody's server); REALITY, WebSocket, TLS,
WireGuard, routing and DNS conversion; the millisecond and negative-expiry traps;
name normalisation including Persian, collisions and the reserved-name seeding;
share-link parsing; the applier against a fake panel — ordering, id
substitution, node pause/resume, conflicts, a full licence, cancellation; and
the whole wizard end to end over HTTP, including its two access guards.

### Still open

1. **Fixtures per upstream version.** The reader tests pin one schema each. A
   3x-ui v2 fixture beside the v3 one would catch the drift that actually
   happens.
2. **Hiddify and Remnawave inbounds.** Neither stores one engine document, so
   only their accounts are converted. Both would need their own config-profile
   readers.
3. **A dry-run against a live Marzban.** The whole pipeline was run against a
   real nexora-panel build (see below); the REST-backed readers — Marzban,
   PasarGuard, Hiddify, Marzneshin, Remnawave — have not yet been pointed at a
   real instance of the panels they read. The backup-fetching channel has been,
   against a stand-in serving a real database on a secret base path.
4. **Translations age.** The five languages are one catalogue in
   `internal/web/assets/i18n.js`; a new string added to the markup shows in
   English everywhere until it is translated, which is the right failure but
   still a failure.

### Verified against a real panel

A 3x-ui fixture — two inbounds (REALITY and WebSocket+TLS), five accounts
including a Persian name and one on both inbounds, an Xray template with
routing, DNS and two outbounds, one admin — was read and applied to a freshly
migrated `nexora-panel` on SQLite. Twelve of twelve items created: the mirrored
rule set, both inbounds, both outbounds, the template (with the inbound,
outbound and rule-set ids resolved), the admin, and all five accounts with their
uuids, quotas and expiry dates intact.

The point of the exercise: `GET /sub/sub1` on the Nexora panel answers **200**
for the subscription id the customer had in 3x-ui. That is the migration
working.

It also found two real bugs that fixtures could not have: sing-box 1.14 removed
the legacy `{"address": …}` DNS server and the `dns` outbound, and the panel
rejects a template carrying either. Both are now converted to the typed form and
pinned by tests. A third fix came out of the same run — a converted
`geosite:`/`geoip:` reference now creates the matching mirrored rule set from
SagerNet's own branches, because a rule naming a tag nothing defines is dropped
by the panel when it builds a node config, and would have looked imported while
quietly not applying.
