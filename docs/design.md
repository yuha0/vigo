# vigo — design

`vigo` controls one Samsung The Frame (LS03D, 2024, Tizen 9) over the TV's local WebSocket API,
serves a web UI for it, and exposes it to Apple Home.

The TV's behaviour is documented in [`tv-api.md`](tv-api.md), observations made on one specific
TV. Everything in this document that depends on TV
behaviour cites that research rather than restating it; read it before changing `internal/samsung`
or `internal/tv`.

## Components

```
 browser / PWA ──Connect──▶ vigo-core (Go) ──wss / REST / D2D TCP──▶ TV
                    │          │   ▲  reverse-proxies /vigo.home.v1.* to vigo-home
                    │          ▼   │  Connect/gRPC, both directions
                    └───────▶ vigo-home (Node, HAP-NodeJS) ──HAP over mDNS──▶ Apple Home
```

| Component | Language | Directory | Owns |
|---|---|---|---|
| vigo-core | Go | `cmd/vigo-core`, `internal/` | TV connection, the database (the only component with access), the public API, integration trust and leadership, serving the web UI |
| vigo-web | TypeScript, React | `web/` | the UI; built into vigo-core with `embed` |
| vigo-home | TypeScript, Node | `home/` | Apple Home accessories. Stateless: its HAP state lives in vigo-core's database, reached only through vigo-core's API |

vigo-home is a client of vigo-core like the browser is. It never talks to the TV or the database.

**No component depends on the Kubernetes API.** Kubernetes provides scheduling, the mounted PKI
directory, database credentials and the encryption keyring, nothing else.

### Why vigo-home is a separate Node process

There is no Go HomeKit implementation we are willing to depend on: `brutella/hap` is unmaintained
since 2024-10 and has an open bug (brutella/hap#67) that breaks iOS ListPairings. HAP-NodeJS
(Homebridge's engine) is actively maintained. Node cannot be embedded in a Go binary in any useful
way: Go JS engines (goja) lack Node's `net`, `dgram`, `http` and `crypto`, which HAP-NodeJS needs.

The split also isolates failures: the TV token and the HomeKit pairing state break and recover
independently, and a HomeKit crash never drops TV control.

### Network requirements

- **The TV must see vigo-core's traffic as coming from its own subnet** (it refuses WebSocket
  clients from anywhere else; `tv-api.md` §1). Either run vigo-core on that subnet or have the
  router masquerade traffic to the TV. With the latter, vigo-core is an ordinary pod; no
  `hostNetwork`.
- **vigo-home needs `hostNetwork`** because HAP advertises over link-local mDNS. Use
  `dnsPolicy: ClusterFirstWithHostNet` so it can resolve the vigo-core Service, and the
  `app.kubernetes.io/component: mdns` label with a pod anti-affinity on it, so it stays off
  nodes running other mDNS advertisers.

| Listener | Component | Clients | Authentication |
|---|---|---|---|
| `:8080` HTTP | vigo-core | browsers, through a private ingress or gateway | none (see Trust) |
| `:8443` HTTPS | vigo-core | vigo-home, other vigo-core replicas | mTLS |
| `:8081` HTTPS | vigo-home | vigo-core | mTLS |

## API contract

**`proto/` is the single source of truth.** Every cross-component call is defined there and code is
generated with `buf`:

| Package | Served by | Called by |
|---|---|---|
| `vigo.core.v1` | vigo-core (`:8080` and `:8443`) | vigo-web, vigo-home |
| `vigo.home.v1` | vigo-home (`:8081`) | vigo-web, through vigo-core's reverse proxy |
| `vigo.integration.v1` | vigo-core (`:8443` only) | integrations such as vigo-home |

Generated code is committed: Go in `gen/go`, TypeScript in `web/src/gen` and `home/src/gen`
(`web` and `home` are independent npm projects with their own lockfiles, so each gets its own
copy). Both must pin the same `@bufbuild/protobuf` version as `protoc-gen-es`. Never
edit generated code; change the `.proto` and run `make gen`.

Transport is [Connect](https://connectrpc.com): vigo-core serves Connect, gRPC and gRPC-Web on one
port. The browser uses the Connect protocol; vigo-home uses gRPC.

### Rules for changing the contract

- Additive changes only within `v1`: new fields, new RPCs, new enum values. Never renumber or reuse
  a field number; mark removed fields `reserved`.
- Every enum's zero value is `*_UNSPECIFIED` and means "unknown". Clients must handle enum values
  they do not recognise.
- A change to `proto/` lands together with the regenerated code in the same change.

### Non-RPC HTTP endpoints on vigo-core

Binary payloads don't suit RPC, so these are plain HTTP:

| Method | Path | Notes |
|---|---|---|
| `GET` | `/art/{content_id}/thumbnail` | `image/jpeg`, cached, `ETag` |
| `GET` | `/library/{artwork_id}/thumbnail` | `image/jpeg`, from the source file or the TV |
| `GET` | `/library/{artwork_id}/image` | `image/jpeg` crop-editor preview, long side at most 2048 |
| `POST` | `/sources/{source_id}/files` | `multipart/form-data`: `file`, optional `path` (directory relative to the source root). Writes the file into the source and starts its upload to the TV. Responds `201` with `vigo.core.v1.LibraryArtwork` as protojson. Errors are `{"code": "...", "message": "..."}` using Connect error code names |
| `GET` | `/healthz` | process is alive |
| `GET` | `/readyz` | ready to serve |
| `GET` | `/metrics` | Prometheus |
| `*` | `/vigo.home.v1.HomeService/*` | reverse proxy over mTLS to the endpoint the `homekit` integration leader registered; `503` (Connect `unavailable`) when none is registered or it is down |
| `GET` | `/*` | the embedded web UI, SPA fallback to `index.html` |

vigo-home serves Connect on `:8081` (mTLS). `/healthz`, `/readyz` and `/metrics` are on a separate
plain-HTTP port on both components (`:9090`) so probes and Prometheus need no client certificate.

### Live updates

`TVService.WatchEvents` is a server stream. Its first message is always a full `Status`; after that
it carries deltas and fresh `Status` snapshots whenever any status field changes. Clients reconnect
with backoff on stream end and treat the first message after reconnect as authoritative.

### Trust

- **Browsers:** no authentication in the application. `:8080` is reachable only through the
  private network. Do not expose it publicly.
- **Between components: mutual TLS.** Every component gets `--pki-dir`, a directory holding
  `tls.crt`, `tls.key` and `ca.crt` (the layout of a cert-manager Certificate Secret; any PKI can
  produce it). Peers must chain to `ca.crt`, and are **authorized by URI SAN**, never by hostname:

  | Identity (URI SAN) | Allowed to |
  |---|---|
  | `spiffe://vigo/core` | call vigo-core's `:8443` (non-leader proxy hop), call integrations |
  | `spiffe://vigo/integration/<kind>` | call `vigo.core.v1` and `vigo.integration.v1` on `:8443` as integration `<kind>` |

  An integration needs no other configuration: a certificate from the system CA with its identity
  is its registration.
- **Hot reload.** Both components watch `--pki-dir` and swap certificates and the CA pool without
  restarting (Go: `tls.Config` `GetCertificate`/`GetClientCertificate`/`VerifyPeerCertificate`
  reading the current material; Node: `setSecureContext` and a fresh client agent). Renewal must
  never require a restart.
- **One CA per system.** In Kubernetes the CA is namespaced: a self-signed `Issuer` bootstraps a
  CA `Certificate`, which backs a CA `Issuer` that signs the leaves, all in the `vigo` namespace.
  A namespaced Issuer can only be referenced by Certificates in its own namespace, so a compromise of another namespace
  cannot mint vigo identities, and nothing cluster-wide is involved beyond cert-manager itself.

## TV semantics the API commits to

These come from verified research and are deliberately reflected in the API shape:

- **"Selected", never "displaying".** Nothing the TV reports says whether the panel is lit.
  `Status.selected_art` is what the TV has selected, which the panel converges to with a lag.
  `Status.showing_art` means only `power == ON && art_mode == ON`.
- **Setters are verified by re-reading.** `UpdateSetting` sends `set_<key>`, then re-reads
  `get_artmode_settings`. `confirmed = false` means the TV acknowledged but the value didn't change.
- **`KEY_POWER` is never sent raw.** Its effect depends on state, and from motion-sleep it powers
  the TV fully off. `SendKey` rejects it. Waking is `ShowArt`, which implements the state-dependent
  wake algorithm.
- **No remote power-off.** Long-press `KEY_POWER` over the websocket (3s and 6s holds) was tested
  and left the TV on. There is no `PowerOff` RPC until a working method is verified.
- **`select_image` omits `category_id`**, and **uploads use one uuid for `id`, `request_id` and
  `conn_info.id`**.
- **Unknown art requests get no reply.** Every art request has a timeout; a timeout is reported as
  `deadline_exceeded`, never retried blindly.
- **Some Art Store items have no thumbnail.** The TV opens the D2D socket for them and closes it
  without data (22 of 106 items on 2026-11-01, consistently per item). That is `404`, remembered for
  ten minutes, and the UI shows a placeholder.
- **D2D sockets are dialed 500ms after the TV announces them.** Connecting immediately fails most of
  the time (verified 2026-11-01; see `internal/samsung/client.go`).
- **No art host** (Art Mode not entered since boot) is a distinct, surfaced state
  (`CONNECTION_STATE_NO_ART_HOST`), not a hang.
- **Model string changes are surfaced loudly** as a `Warning`, since the REST `model` encodes the
  Tizen generation and firmware changes are the main operational risk.

## State and storage

**All state lives in PostgreSQL, owned by vigo-core.** Nothing is written to the filesystem.
vigo-home persists through `vigo.integration.v1` `ListState`/`PutState`/`DeleteState`.

- Driver `pgx`, schema migrations embedded and applied at startup under the leader lock.
- Connection via `--database-url-file` (a libpq URI; in Kubernetes CloudNativePG's `<cluster>-app`
  Secret, key `uri`) with `sslmode=verify-full` against the cluster CA.
- The application role owns only its database.

| Table | Contents |
|---|---|
| `tv_credentials` | TV token (encrypted), client name, `created_at`, `last_verified_at`, `invalidated_at` |
| `config` | runtime overrides of TV host, port and client name |
| `selection` | the selection record (see `internal/tv/selection.go`) |
| `uploads` | own upload tally, content id → bytes |
| `tv_items` | mirror of the TV library from `get_content_list` |
| `sources`, `source_items` | configured sources; files with path, hash, size, mtime, upright dimensions |
| `artworks`, `artwork_tags` | the library (see Art library and sources) |
| `model_history` | REST model strings seen, for the model-change warning |
| `leader` | the current vigo-core leader's identity and `:8443` address |
| `integrations` | per kind: leader instance, registered endpoint, certificate identity, last seen |
| `integration_state` | per kind, key → value (encrypted) and version |

### Encryption of secrets

Values that grant access are encrypted by vigo-core before they reach the database: the TV token
and every `integration_state` value (HAP state holds long-term private keys and paired
controllers).

- AES-256-GCM, a random 96-bit nonce per write, and additional authenticated data binding the
  ciphertext to its table, column and row key, so a value cannot be moved to another row.
- Each value records the id of the key that encrypted it. The keyring comes from
  `--keyring-file` (JSON: a primary key id and base64 32-byte keys), never from the database, so
  dumps and backups are useless without it. Rotation: add a key and make it primary; new writes use
  it, old ones still decrypt, and vigo-core re-encrypts old rows in the background.
- In Kubernetes the keyring is a Secret that must be backed up outside the cluster (for example
  as a SealedSecret in git): it must outlive the cluster,
  because restoring a database backup needs it.

The TV token and the HAP pairing state need a human to recover. Never delete either
automatically.

## Leader election

**PostgreSQL advisory locks.** Off by default (`--leader-elect`); with it off the process is always
active, which is how a single replica runs. No Kubernetes objects are involved.

- **vigo-core:** every replica tries `pg_try_advisory_lock` on a dedicated session. The holder is
  leader: it holds the TV connection and writes its `:8443` address to `leader`. If its session
  dies, Postgres releases the lock.
- **Every vigo-core replica serves the API.** A non-leader reverse-proxies each request to the
  leader over mTLS. All replicas stay Ready, so rolling updates never wait on leadership.
- **Integrations:** `IntegrationService.AcquireLeadership` is a stream; vigo-core backs each stream
  with an advisory lock (keyed by integration kind) on its own database session. A replica acts as
  leader only while its stream reports `LEADER`.
- **On losing leadership, exit.** A process that stops being leader terminates and restarts as a
  standby. No in-process demotion paths.

`replicas: 1` must work, but no code may assume it: no unguarded writes, no "only one of me"
shortcuts.

## Art library and sources

vigo keeps its own index of artworks (`vigo.core.v1.LibraryService`): everything on the TV plus
everything in configured **sources**, with tags and a sync state per artwork.

### Identity

| Artwork | Id |
|---|---|
| content from a source | `sha256:<hex of the file bytes>` |
| on the TV only (Art Store, uploads made outside vigo) | `tv:<content_id>` |

Content addressing means renames and moves never re-upload, identical files are one artwork, and an
edit (crop) is a new artwork that replaces the old one on the TV.

### Tags

Stored in `artwork_tags (artwork_id, tag, origin)`, primary key on the first two, indexed on `tag`,
rather than jsonb: exact and prefix filters, counts with `GROUP BY` and set semantics come for free.

| Tag | Origin | Meaning |
|---|---|---|
| `source/art-store` | system | Samsung content (`server`, `preinstall`) |
| `source/upload` | system | on the TV, uploaded outside vigo's sources |
| `source/<source-id>` | system | from that vigo source |
| `folder/<dir>` | system | directory within a filesystem source, relative to its root |
| `tv-category/<id>` | system | the TV's `category_id` (opaque; the TV reports no names) |
| `status/on-tv` | system | currently on the TV |
| `issue/low-resolution` | system | upright width < 3840 or height < 2160; the TV will upscale |
| `issue/aspect-ratio` | system | more than 1% from 16:9 |
| anything without `/` | user | edited in the UI |

### Sources

A source is a store of image files outside the TV. Flag `--source <url>`, repeatable. The source id
comes from the `id` query parameter, else a short hash of the URL; keep it stable, it is part of tags
and of the cache layout.

| Capability | Meaning | `file://` |
|---|---|---|
| scan | list files with size and modification time | walk the tree; skip hidden entries; JPEG and PNG |
| read | open a file | yes |
| write | accept browser uploads | yes |
| edit | replace a file in place (crop) | yes, atomic rename |

`--cache-dir/<source-id>/` holds generated thumbnails, crop previews and prepared uploads. Only the
leader uses it, and it is emptied at startup. The database is the source of truth; the cache is
disposable.

### Sync

Sources and the TV are kept in sync **both ways** for artworks that came from a source and that vigo
uploaded ("linked"). TV-only artworks are never written to a source.

| Change | Result |
|---|---|
| file added to a source | uploaded to the TV |
| file changed (new content) | new artwork uploaded, old TV copy deleted |
| file removed from a source, or deleted in vigo | TV copy deleted |
| linked artwork deleted on the TV | file deleted from its source |
| browser upload | file written to the chosen source, then uploaded right away |

The reconciler runs on the leader per source every `--source-sync-interval` (default 10 minutes), on
`SyncSource`, and after uploads and edits. The TV library is mirrored in `tv_items` from
`get_content_list` periodically, on `image_added`/`image_deleted`, and on `Refresh`.

Correctness rules, because sync deletes user files:

- **"Deleted on the TV" needs proof:** an `image_deleted` event, or absence from two consecutive
  complete content listings. A failed or partial listing never counts.
- **vigo's own deletions are recorded before they are sent,** so their disappearance from the TV is
  never read as a deletion on the TV.
- **Only uploads vigo made are ever deleted from the TV.**
- **No deletion threshold.** A user deleting every file is a valid choice, and an arbitrary
  percentage is not a correctness rule. Correctness comes from the rules above: deletions happen only
  on proof, never on a failed or partial observation.
- A source whose root is missing or unreadable is an error, never an empty source.

Sync states: `IN_SYNC`, `PENDING_UPLOAD`, `UPLOADING`, `PENDING_DELETE`, `DELETING` (both shown
greyed out until the TV confirms), `ERROR` with the reason, and `TV_ONLY`.

### Editing

`CropArtwork` takes a rectangle in upright original pixels, crops, re-encodes in the file's own
format (JPEG quality 92, or PNG for a PNG, so the name always matches the content; EXIF
orientation baked in, metadata stripped) and **overwrites the file in place** through the
source's edit capability, as one atomic replace. Offered in the UI for `issue/aspect-ratio`
artworks whose source is editable; the editor fixes the rectangle at 16:9.

### Browser uploads

`<input type="file" accept="image/*">`, which on iOS opens the system photo picker. An installed
iOS PWA can receive HEIC, which vigo-core cannot decode; the browser converts HEIC to JPEG with
`createImageBitmap` and a canvas before uploading, capping the long side at 4096 pixels (iOS canvas
limit; still above 3840).

## Observability

No in-app diagnostics page; vigo emits what standard tooling collects:

- **Logs:** structured JSON to stdout (`log/slog` in Go, `pino` in Node). Every TV request logs request name, duration and outcome. Never log the token.
- **Metrics:** Prometheus at `/metrics`, scraped through a ServiceMonitor. All names start with
  `vigo_`.

Required vigo-core metrics:

| Metric | Type | Labels |
|---|---|---|
| `vigo_tv_power_state` | gauge (1 for the current state) | `state` |
| `vigo_tv_art_mode` | gauge | `state` |
| `vigo_tv_connection_state` | gauge | `state` |
| `vigo_tv_requests_total` | counter | `request`, `outcome` (`ok`, `error`, `timeout`) |
| `vigo_tv_request_duration_seconds` | histogram | `request` |
| `vigo_tv_setting_unconfirmed_total` | counter | `key` |
| `vigo_tv_protocol_errors_total` | counter | `kind` (unexpected message shapes; firmware-change signal) |
| `vigo_tv_reconnects_total` | counter | `channel` |
| `vigo_tv_d2d_bytes_total` | counter | `direction` |
| `vigo_tv_d2d_duration_seconds` | histogram | `direction` |
| `vigo_leader` | gauge | |

Also on vigo-core: `vigo_db_up`, `vigo_integration_leader` (gauge, label `kind`, 1 while a leader
stream is held), `vigo_tls_cert_expiry_seconds` (gauge, label `listener`).

Required vigo-home metrics: `vigo_home_leader`, `vigo_home_paired` (gauge, label `group`),
`vigo_home_core_connected`, `vigo_home_characteristic_writes_total` (labels `accessory`,
`outcome`), `vigo_tls_cert_expiry_seconds`.

## Apple Home

vigo-home publishes through a `Provider` interface (`home/src/provider.ts`) so the protocol can be
swapped. `homekit` is implemented with HAP-NodeJS; `matter` is a stub that reports
`implemented: false` (no TV device type is surfaced by Apple Home over Matter).

vigo-home is stateless. HAP-NodeJS reaches storage only through `getItem`, `setItemSync` and
`removeItemSync` on `HAPStorage.storage()`; vigo-home replaces that store with one preloaded from
`ListState` before HAP starts and written through to `PutState`/`DeleteState`. Characteristic
reads come from vigo-core's event stream, and writes are vigo-core RPCs.

### Pairing groups

A HomeKit Television accessory must be published **unbridged** to get a TV tile and appear in the
iOS Control Center remote. So there are two independent pairings:

| Group | Contents | Advertised |
|---|---|---|
| `tv` | the Television accessory | always (when vigo-home is active) |
| `bridge` | optional virtual accessories | only while at least one is enabled |

Each group has its own setup code and `X-HM://` setup URI; the web UI renders the QR code from it.

### Accessories

| id | Group | HomeKit service | Behaviour |
|---|---|---|---|
| `television` | `tv` | Television + TelevisionSpeaker + InputSources | Active=1 → `ShowArt`. Active=0 → no-op, logged (no verified power-off). Active reads `power == ON`. Inputs: `art` (Art Mode) → `SetArtMode(true)`; `tv` → `SetArtMode(false)`. RemoteKey → `SendKey`. Speaker volume → `KEY_VOLUP`/`KEY_VOLDOWN`/`KEY_MUTE` |
| `keep_art_on` | `bridge` | Switch | On → `motion_timer = off`. Off → restore the last non-off value (default `5`). Reads on iff `motion_timer == off` |
| `art_mode` | `bridge` | Switch | `SetArtMode` |
| `art_brightness` | `bridge` | Lightbulb (brightness only) | 0–100% ↔ setting `brightness` range, rounded |

HomeKit RemoteKey mapping:

| RemoteKey | TV key |
|---|---|
| ARROW_UP / DOWN / LEFT / RIGHT | `KEY_UP` / `KEY_DOWN` / `KEY_LEFT` / `KEY_RIGHT` |
| SELECT | `KEY_ENTER` |
| BACK | `KEY_RETURN` |
| EXIT | `KEY_EXIT` |
| PLAY_PAUSE | `KEY_PLAY` |
| INFORMATION | `KEY_INFO` |

Only `KEY_UP`, `KEY_ENTER`, `KEY_HOME` and `KEY_VOLUP` have been observed reaching the tested TV. Verify
every other key against the TV before relying on it.

## Web UI

React 19 + Vite + TypeScript + Tailwind + shadcn/ui, TanStack Query over the generated Connect
client, installable as a PWA.

- **Home:** one layout in every TV state: the navigation rail and a TV card with a fixed-size
  preview (the selected artwork's thumbnail in Art Mode, labelled "Selected"; a placeholder
  otherwise), the state, and the actions that help. Calls `TVService.Refresh` on load and whenever
  the page becomes visible.
- **TV:** art-mode settings generated from `ListSettings` (range → slider, enum → segmented
  control or select, toggle → switch); art mode on/off; Show Art; TV host/port/client name;
  pairing flow with "press Allow on the TV" guidance. Unconfirmed setter results are shown, not
  hidden.
- **Gallery:** the library from `LibraryService`: grid with sync-state badges (deleting artworks
  greyed out until confirmed), tag filter (grouped by namespace) and search, select on the TV
  (the badge moves only when the TV confirms), issue badges, a crop editor for
  `issue/aspect-ratio` artworks in editable sources, user tag editing, upload into a writable
  source, delete (syncs to source and TV), and per-source status with Sync now.
- **Integrations › Apple Home:** per pairing group, a QR code and setup code when unpaired; accessory
  enable toggles; reset pairing behind an explicit confirmation.
- **Remote:** on iOS, an "Open iPhone Remote" button (`tvremote://`, unverified, iOS 16+) above
  the virtual remote; elsewhere just the virtual remote. The virtual remote has no power key.

## Repository layout

```
proto/vigo/{core,home,integration}/v1/  API contract (buf)
gen/go/                      generated Go code, committed (TS goes to web/src/gen, home/src/gen)
cmd/vigo-core/               vigo-core entrypoint (cobra)
internal/
  samsung/                   TV wire protocol: wss + Tizen 9 handshake, pairing, art and remote
                             channels, D2D transfers, REST, port probe. No policy.
  tv/                        domain service: state model, wake algorithm, setter verification,
                             selection tracking, polling, event fan-out, KEY_POWER guard
  imageprep/                 EXIF orientation, pad/crop to 3840x2160, JPEG encode
  library/                   artwork index, tags, TV mirror, reconciler
  source/                    Source interface; file/ implementation
  db/                        pgx pool, embedded migrations, queries
  secretbox/                 keyring and AES-GCM envelope encryption
  leader/                    advisory-lock election and leader address
  pki/                       --pki-dir loading, hot reload, SAN authorization
  integration/               IntegrationService: leadership streams, registry, state
  coreapi/                   Connect handlers, HTTP endpoints, leader and home proxies, web embed
  metrics/                   Prometheus collectors
web/                         vigo-web
home/                        vigo-home (provider, homekit, matter, rpc, storage, pki, metrics)
deploy/kustomize/            manifests for the vigo namespace (CNPG, issuers, apps)
```

## Development

Requirements: Go 1.27, Node 26, `buf`, `protoc-gen-go`, `protoc-gen-connect-go` and
`protoc-gen-es` on `PATH`, `openssl`, and a PostgreSQL 18 for the database-backed tests and local
runs (its URL in `.dev/database-url`). `make help` lists targets: `make dev-pki` creates a throwaway
CA and certificates under `.dev/`, `make dev-art` seeds sample images, and `make dev-core
ARGS=--fake-tv` runs vigo-core without a TV.

Live TV tests must run from a host the TV accepts WebSocket clients from (its own subnet, or
behind the masquerade); from anywhere else only the TV's REST API answers.
