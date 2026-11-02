# Samsung The Frame local API: research notes

> **Read this first.** These notes record behaviour observed on **one specific TV** (below). The
> local API is **reverse-engineered and unofficial**: Samsung does not document it, and can change
> it with any firmware update. Treat everything here as "verified on this TV and firmware on the
> date given", not as a specification.

Code in this repository cites these notes as `docs/tv-api.md §N "Heading"`.

---

## 0. Quick reference — start here

Everything below section 0 was verified against the real TV on 2026-10-31/11-01. Nothing in this
document is speculative unless it says so.

### The target

| | |
|---|---|
| TV | Samsung The Frame **LS03D 2024**, `QN75LS03DAFXZA`, 75" |
| Firmware | `T-PTMDAKUC-0090-2130` (**Tizen 9**), Auto Update **off** |
| REST `model` | `24_PTM_FTV_T09` (the `_T09` suffix means Tizen 9) |
| Network | wired, fixed address (DHCP reservation) |

### Working reference implementations

Before vigo, every flow below was proven with small standalone Python scripts (standard library
only, plus Pillow for image preparation) run against this TV: pairing with the Tizen 9 register
frame, a generic art-channel request/response harness, upload and thumbnail download over D2D
sockets, EXIF-correct resize/pad to 3840x2160, remote-channel key presses including long-press,
panel wake attempts, and periodic state sampling. vigo's `internal/samsung` package implements the
same flows.

### Non-negotiable environment

- **The client must appear to be on the TV's subnet.** Samsung refuses WebSocket clients from
  other subnets. A masquerade/SNAT rule on the router for traffic to the TV solves it. See §1.
- **IP Remote must be ON** on the TV, or it leaves the network a few minutes after sleeping. See §4.
- **Access Notification = First Time Only** for pairing.

### The five things that will waste your time

1. **Off-subnet = instant refusal.** Samsung refuses websocket clients outside the TV's subnet.
   Making the client appear on the TV's subnet is what makes this work at all.
2. **Tizen 9 needs the register frame first.** The TV does not volunteer `ms.channel.connect`; send
   `ms.channel.connect` yourself, tolerate `ms.error: No Authorized`, keep the socket open.
3. **Upload: `conn_info.id` must equal `id` and `request_id`.** Otherwise the TV accepts the bytes
   and silently discards the image. No error.
4. **`select_image` must omit `category_id`**, or you get `error_code: -7`.
5. **Setters are `set_<item>`.** `set_artmode_settings` accepts anything and does nothing.

### What the TV will not tell you

- **Whether the panel is lit.** Art-Mode-lit and Art-Mode-dark are identical on every readable
  field. A client can honestly say "currently *selected* X", never "currently *displaying* X".
- **Storage usage.** Only `tv_flash_size: 16` (GB, whole device). Track your own uploads.
- **Anything about unknown requests.** Unsupported request names get **no reply at all**, so you
  cannot probe the API by looking for errors.

---

## 1. The TV's local API

Two separate interfaces on the same host, on ports **8001 (plaintext)** and **8002 (TLS)**.

### REST — no auth, no pairing

```
GET http://<tv>:8001/api/v2/
GET https://<tv>:8002/api/v2/     # self-signed cert; clients skip verification
```

Returns device info JSON. Fields seen on a real Frame
([fixture](https://github.com/NickWaterton/samsung-tv-ws-api/blob/66a4d5bceb85e228dd3b6d15697ac1ae960f84f3/tests/fixtures/devices/2022_frame_QN43LS03B.json)):

```json
"FrameTVSupport": "true",   "TokenAuthSupport": "true",
"PowerState": "on",         "OS": "Tizen",
"developerMode": "0",       "developerIP": "<ip>",
"smartHubAgreement": "true","firmwareVersion": "Unknown",
"model": "22_PONTUSM_FTV",  "modelName": "QN43LS03BAFXZC",
"networkType": "wired"
```

Use this for liveness checks, power state, and model identification. Note `firmwareVersion` reads
`Unknown` — **the REST API does not report the firmware version.**

**Per-app queries work** (verified 2026-11-01): `GET /api/v2/applications/<app id>` answers for a
given app id. The websocket's `ed.installedApp.get`, which some libraries use to list installed
apps, **gets no reply on Tizen 9** (verified 2026-11-01), so there is no known way to enumerate
apps; you must know the id.

### WebSocket — the control channel

```
wss://<tv>:8002/api/v2/channels/<channel>?name=<base64>&token=<token>
```

Two channels matter:

| Channel | Purpose |
|---|---|
| `samsung.remote.control` | key presses, power, input |
| `com.samsung.art-app` | art mode: list/select/upload art, matte, brightness |

**This API is not documented by Samsung.** Every implementation is reverse-engineered by
observation. Samsung's supported third-party route is the SmartThings *cloud* API, which requires
internet and an account — the opposite of a local-only design.

It is not, however, a hidden or exploited interface: it has an on-screen consent prompt, issues
tokens, and lists paired clients under Device Connection Manager. Samsung's own phone apps use it.
Third parties simply aren't documented users of it.

Precedent worth knowing: Samsung **removed the art API in 2022 and restored it in firmware 1622**
for post-2021 Frames ([openHAB binding](https://openhab.org/addons/bindings/samsungtv)). The
interface genuinely is at Samsung's mercy — but historically breakage has been temporary.

### Pairing

A one-time, physically-present authorization that yields a bearer token:

1. Client opens the websocket with a client name in the URL.
2. **The TV displays an on-screen prompt.** A human presses Allow on the physical remote.
3. The TV returns `ms.channel.connect` containing `data.token`.
4. The client stores the token and appends `&token=…` to every later connection.

Consequences:

- **The token must outlive the client process.** Store it durably; losing it means a human at the
  TV again.
- **The TV can forget tokens** after a long power-off. Treat this as an expected, human-in-the-loop
  event, not a retryable error.
- **On 2024 Frames the art channel shows no prompt of its own.** Pair on `samsung.remote.control`
  first, then reuse the token for art
  ([README](https://github.com/NickWaterton/samsung-tv-ws-api/blob/66a4d5bceb85e228dd3b6d15697ac1ae960f84f3/README.md#L247)).
- A UI should expose an explicit **Pair** action that tells the operator to go press Allow.

### Art upload opens a second connection

For uploads and thumbnails the TV replies with `conn_info {ip, port, key, secured}` and the client
opens a **second outbound TCP connection to a random high port** on the TV
([code](https://github.com/NickWaterton/samsung-tv-ws-api/blob/66a4d5bceb85e228dd3b6d15697ac1ae960f84f3/samsungframetv/art.py#L609-L641)).
Observed ports in test fixtures include 51317 and 57419.

**The TV never connects back to the client.** All connections are outbound from the client to the
TV.

### Other interfaces on the TV

Not used by vigo; recorded so nobody has to rediscover them (verified 2026-11-01):

- **UPnP on port 9197** is a standard UPnP MediaRenderer. It has **no input-source action**, so it
  cannot switch inputs or art mode. Its port is still useful: see §4 "Consequences for vigo".
- **IP Control on port 1516** (Samsung's JSON-RPC control protocol) exists, is gated by the IP
  Remote setting, and **needs a separate pairing** of its own. It is unexplored.

### Hard constraint: the TV refuses off-subnet WebSocket connections

**Samsung TVs reject WebSocket connections whose source address is outside the TV's own subnet.**
Documented upstream under
[Known issues and restrictions → Subnet / VLAN](https://github.com/xchwarze/samsung-tv-ws-api#subnet--vlan):

> Samsung Smart TVs do not allow WebSocket connections across different subnets or VLANs. […]
> Depending on the network setup, this limitation might be mitigated using: IP masquerading (NAT),
> a proxy.

Verified directly against this TV on 2026-10-31:

| Source | Token | Result |
|---|---|---|
| Different subnet | none | `ms.channel.timeOut`, closed in **1s**, no on-screen prompt |
| Different subnet | none, register-frame-first | `ms.error: No Authorized`, closed in 1s |
| Different subnet, port 8001 | none | `ms.channel.unauthorized`, closed |
| **Same subnet** | none | **prompt appeared, token issued at 4.9s** |
| Different subnet | **valid token** | `ms.channel.timeOut`, closed in **0.6s** |

**A valid token does not exempt you.** The restriction applies to every connection, not just
pairing. REST on 8001/8002 is unaffected and works from anywhere.

### SOLVED: gateway-level masquerade

**Make the client appear on the TV's subnet with one NAT rule on the router.** A router with an
address on the TV's subnet can masquerade (source-NAT) traffic from the client's network to the
TV's address, so the TV sees the router's own address on its subnet. Verified 2026-10-31.

Match on the TV's address with all protocols and ports. Consequences, all verified:

- **The client needs no special networking.** It can run anywhere that routes to the TV, including
  as an ordinary container; no host networking or interfaces on the TV's subnet.
- **Arbitrary destination ports work for free.** Because the rule matches the TV's address for all
  protocols, the random high ports of D2D transfers (thumbnails, uploads) need no configuration. **No
  proxy is needed.**
- **Firewall policy is unaffected** on routers that apply NAT after the forwarding decision: zone
  rules still see the original source.
- **The token binds to the masqueraded source address,** not to the client, so it keeps working
  when the client moves between machines.

**Two dependencies this creates:** the TV needs a fixed address (the rule names it), and the rule's
source match must cover wherever the client runs.

---

## 2. Developer Mode — ruled out, do not revisit

Developer Mode opens SDB on TCP 26101. **It is not a control channel.** On retail firmware
`sdb capability` reports:

```
intershell_support : disabled     ← no shell
sdbd_rootperm      : disabled     ← no root
filesync_support   : push         ← push only, no pull
appcmd_support     : disabled
```

Only `shell 0 applist`, `shell 0 getduid`, `uninstall <pkgid>`, plus install and push, actually work
([reference](https://github.com/ardazeytin/samsung-tizen-tv-debloat)). **There is no SDB command for
art mode, power, or input.** Those live only on the websocket.

What it genuinely unlocks is installing your own Tizen app onto the TV. That is a different project
with a different risk profile, and it is **out of scope**.

Two further reasons not to enable it:

- **It pins access to a single Host PC IP.** The TV only accepts SDB from the address entered in the
  dialog. That fights directly with clients whose address changes.
- It adds an attack surface and a support-status question for zero functional gain here.

> Caveat: the capability table above was verified on a 2018 Tizen 4.0 set, not a 2024 one. If this
> ever needs revisiting, `sdb -s <ip>:26101 capability` is the one command that settles it.

**Rooting is explicitly rejected.** A vulnerability-based approach is not a sustainable foundation,
gets patched, and risks bricking a TV that cannot be downgraded.

---

## 3. Firmware: what we actually learned

**The TV now runs Tizen 9, `T-PTMDAKUC-0090-2130`, and everything works.** The earlier fear that
Tizen 9 broke the WebSocket API was wrong, and the reasoning that produced it is worth recording so
it is not repeated.

### The Tizen 9 scare was mostly a misdiagnosis

Three GitHub reports appeared to show Tizen 9 breaking pairing. Two of them do not survive scrutiny:

- **[core#179510](https://github.com/home-assistant/core/issues/179510)** and
  **[xchwarze#184](https://github.com/xchwarze/samsung-tv-ws-api/issues/184)** describe an instant
  `ms.channel.timeOut` with no on-screen prompt. **That is the signature of an off-subnet client**,
  not of Tizen 9 — reproduced on this TV while it was still on Tizen 8. The "Samsung broke it to
  force SmartThings" comment on #184 had no evidence behind it, and that project's own README
  documents the subnet restriction a few sections away.
- **[ollo69#437](https://github.com/ollo69/ha-samsungtv-smart/issues/437)** was *correct*, and its
  finding is the one real Tizen 9 change. See below.

### The one real change: the handshake order

**On Tizen 9 the TV does not volunteer `ms.channel.connect`.** The socket opens and stays silent.
The client must speak first:

```json
{"method":"ms.channel.connect",
 "params":{"deviceName":"<base64 name>","id":"<name>","type":"native","token":"<token or empty>"}}
```

The TV answers `{"event":"ms.error","data":{"message":"No Authorized"}}` and **keeps the socket
open**; the token arrives on that same socket in `ms.channel.connect` → `data.token` once the user
presses Allow. Verified on this TV: token issued 6.3 seconds after Allow.

**This is why the reference libraries fail.** Their `open()` waits to *receive* `ms.channel.connect`
first and treats any other event as fatal, so it never sends the register frame and never waits
through `No Authorized`. A client must implement the register-frame flow.

Once a valid token exists, the **plain handshake works normally** — no register frame needed for
ordinary reconnects.

### Port 8002 filtering — train-specific, not build-specific

[kohlerryan#9](https://github.com/kohlerryan/samsung-tv-art-uploader/issues/9) reports Tizen 9
firmware filtering port 8002 while 8001 stays open, on `T-PTMDDEUC-2123` (an EU train).
**This did not reproduce here** — 8002 is open on 2115, 2123 and 2130 of the US `T-PTMDAKUC` train.

A client should still **make the port configurable**, defaulting to 8002 and able to fall back to
8001. It is a cheap hedge against a real, documented failure mode. Note 8001 is plaintext, though
the 8002 certificate is self-signed and unverified anyway.

### Firmware policy

- **Auto Update stays off.** Updates are a deliberate act.
- **Downgrade is impossible.** Samsung's
  [developer FAQ](https://developer.samsung.com/smarttv/develop/faq/device-information-and-firmware.html)
  states plainly: *"No. For security reasons, downgrading the TV firmware is not supported."*
  **A factory reset does not revert firmware** — it restores settings only. There is no recovery
  image and USB sideloading an older build is refused.
- **Record the firmware/library pair that works.** Current known-good: `0090-2130` with the
  register-frame client in this repository.

### Token lifetime across upgrades

- Tizen 8 → Tizen 9: **token invalidated**, re-pairing required.
- 2123 → 2130 (within Tizen 9): **token survived**.

Re-pairing needs someone at the TV to press Allow, but **not** a client physically on the TV's
subnet — the masquerade path covers pairing as well.

---

## 4. Established facts about this TV

Verified 2026-10-31 against the actual unit.

| | |
|---|---|
| Model | `QN75LS03DAFXZA` — 75" The Frame, US |
| **Firmware** | **`T-PTMDAKUC-0090-2130` (Tizen 9)** |
| REST `model` | `24_PTM_FTV_T09` |
| Auto Update | off |
| Developer Mode | off; **Tizen 9 also closed port 26101** |
| Smart Hub terms | accepted |
| Connection | wired, fixed address |
| Client name | `vigo` |

**The `model` string encodes the Tizen generation** — proven by a controlled before/after on this
one TV: `24_PONTUSM_FTV` on Tizen 8 became `24_PTM_FTV_T09` after the upgrade. REST needs no
pairing, so this is a cheap way to identify a TV's generation. Note `firmwareVersion` always reads
`Unknown`; the real version only appears in Settings → Support → About This TV.

### Verified capability matrix

| Check | Tizen 8 (1310.1) | Tizen 9 (2130) |
|---|---|---|
| REST on 8001 / 8002 | yes | yes |
| Port 8002 | open | open (not filtered) |
| Port 26101 (SDB) | open | **refused** |
| Port 1516 (IP Control) | open with IP Remote on | refused (IP Remote off) |
| Plain handshake, valid token | connects | connects |
| Pairing | prompt on connect | **requires register frame** |
| Art channel read | yes | yes |
| Art channel **write** | — | **yes** (`set_artmode_status`, all `set_<item>` setters, `select_image`) |
| Thumbnail download (D2D socket) | — | **yes**, random high port, via the masquerade |
| On-screen Art menu | usable | **blocked by Samsung account login** |

### TV settings that matter

- **Device Connection Manager → Access Notification → First Time Only**, and clear the Device List.
- **IP Remote is REQUIRED — turn it on.** While the TV is *awake* it appears to gate only port 1516
  (Samsung's IP Control JSON-RPC, see §1 "Other interfaces on the TV"), which vigo does not use —
  testing it awake wrongly suggests it is unnecessary. **While the TV is asleep it keeps the network
  stack alive.** Measured A/B over 25-minute probes at 30s intervals, same conditions, only this
  setting changed:

  | IP Remote | Result |
  |---|---|
  | **on** | 20 minutes of continuous standby, **zero** unreachable samples |
  | **off** | **every** sample unreachable — no ICMP, all ports timing out |

  With it off the TV holds the network for a few minutes after sleeping, then drops off entirely and
  can only be recovered with the physical remote. With it on, standby stays fully reachable.
  **This is the single setting that decides whether a client is "normally connected" or "normally
  disconnected".**
- **Power On with Mobile** — untested. Its purpose is keeping the network alive in standby, which
  matters only for remote power-on. Note The Frame normally rests in Art Mode, where it is fully
  networked, so this may rarely matter.

### Power, panel and art-mode state model

**The single most important distinction: "the TV is awake" and "the panel is lit" are different
things.** In Art Mode the panel is gated by the motion sensor — the TV can report `PowerState: on`
and `artmode_status: on` with the screen completely black because nobody is in the room.

#### Observable states

| State | `PowerState` | `artmode_status` | 9197 | 8002 | Panel |
|---|---|---|---|---|---|
| Normal TV mode | `on` | `off` | open | open | **lit** (not motion-gated) |
| Art Mode, motion present | `on` | `on` | open | open | **lit** |
| Art Mode, no motion | `on` | `on` | open | open | **DARK** ← indistinguishable via API |
| Motion-sleep | `standby` | `on` | refused | open | dark |
| Power-off (long-press) | `standby` | `off` | refused | open | dark |
| Any standby, IP Remote off | unreachable after a few min | — | timeout | timeout | dark |

**Rows 2 and 3 are identical on every field we can read.** There is no known API signal that
reports whether the panel is actually illuminated. Do not claim "currently displaying X" in a UI
without acknowledging this.

#### Transitions and triggers

| From | Trigger | To |
|---|---|---|
| Art Mode, lit | `motion_timer` elapses with no motion | panel dark (TV still awake) |
| Art Mode, dark | **motion detected** | panel lit |
| Art Mode, dark | **`select_image`** | panel lit (verified 2026-11-01) |
| Art Mode, lit/dark | longer idle | motion-sleep (`standby`) |
| Motion-sleep | **motion detected** | Art Mode, lit |
| Motion-sleep | **`select_image`** | awake (use `set_artmode_status on` to land in Art Mode) |
| Motion-sleep | `set_artmode_status on` | **nothing** — no-op, art mode already `on` |
| Motion-sleep | **`KEY_POWER`** | ⚠ **powers the TV fully OFF** |
| Power-off standby | **`KEY_POWER`** | awake in Art Mode, but **panel dark until motion** |
| Power-off standby | `select_image` / `set_artmode_status` | **nothing** |
| Awake | `KEY_POWER` | toggles normal ⇄ Art Mode, or powers off |
| Awake | **long-press `KEY_POWER`** over the websocket (3s and 6s holds) | **nothing** — the TV stays on (verified 2026-11-01) |
| Any standby | IP Remote off + a few minutes | unreachable; physical remote only |

**There is no known remote power-off.** The physical remote's long-press power-off cannot be
reproduced over the websocket.

#### The motion timer is the panel gate — and a client controls it

**Confirmed by measurement + visual check (2026-10-31).**

| `motion_timer` | TV state over 25 min | Panel |
|---|---|---|
| `5` (minutes) | awake ~5 min, then `standby` for the rest | dark after the timer; motion relights it |
| **`off`** | **awake for all 50/50 samples, never `standby`** | **art displayed the entire time** |

So the motion timer governs both the sleep *and* the panel. With it off the TV stays awake and the
panel stays lit indefinitely with no motion at all. The ambient brightness sensor and Night Mode were
ruled out as the cause — the confirming run was late at night in a dark room with
`brightness_sensor_setting: on`, and the art displayed fine throughout.

**This is the lever that makes it possible to display art on demand**, and it resolves the
"can the app put something on screen?" question in the good direction.

**Day/night pattern.** `motion_timer: off` means no night-off, which is usually not what you want
overnight. Since `set_motion_timer` works, a client can schedule it:

```
daytime  -> set_motion_timer off     art reliably displayed and controllable
night    -> set_motion_timer 5       panel darkens when the room is empty
```

That gets both behaviours without compromise.

#### Consequences for `vigo`

- **`PowerState` distinguishes standby from awake; `artmode_status` distinguishes which sleep you are
  in. Neither tells you the panel is lit.**
- **Port 9197 is a precise awake probe** — flips to refused exactly when `PowerState` becomes
  `standby`. Cheaper than a REST call for polling.
- **`KEY_POWER` is a toggle and is state-dependent.** Never send it without reading
  `artmode_status` first: in motion-sleep it powers the TV completely off.
- **Waking is transient.** With `motion_timer` at 5 minutes and nobody in the room, the TV re-sleeps
  five minutes after any wake. A wake buys a short window, not a lasting state.
- **In Art Mode the motion timer owns the panel.** A client CAN put art on screen on demand by
  setting `motion_timer: off`; schedule it back to `5` at night for the dark-room behaviour.
- An acknowledgement from the art API is never proof of a visible effect.

### Art channel quirks

- **The clients list can contain MORE THAN ONE `isHost: true` entry** (two "Smart Device" entries
  observed after a reboot). Do not assume a single host.

- **The art app registers as host only after Art Mode has been entered since boot.** Until then the
  clients list has no `isHost: true` entry and every `art_app_request` addressed `"to": "host"` is
  silently dropped — no error, no reply. Once registered it **persists** after leaving Art Mode.
  A client must detect a missing host and surface it rather than hanging.
  **Open question:** whether art mode can be turned *on* remotely after a reboot, when no host is
  registered. Possible workaround is `KEY_POWER` on the remote channel, which toggles Art Mode on
  The Frame — untested.
- **The TV closes idle art connections after ~18 seconds.** Expect this and treat reconnection as
  routine, or keep the channel alive.
- `get_api_version` never returns a response; `get_artmode_status` does.

### Still to verify

1. ~~Art upload~~ — **done**, verified end to end.
2. ~~What state motion-sleep lands in~~ — **answered**: `PowerState: standby` with the network kept
   alive when IP Remote is on (see "The motion timer is the panel gate" and "TV settings that
   matter").
3. **Power On with Mobile** — strongly implicated in remote wake but not cleanly proven; re-enable
   it and repeat the power cycle.
4. Whether Art Store content keeps rendering while the TV is offline and unauthenticated.

---

## 5. The art API in practice

Everything below was exercised against the real TV on Tizen 9 `0090-2130`, from a client on another
subnet through the router masquerade (§1).

### Samsung gates the UI, not the local API

**After the Tizen 9 upgrade, the on-screen Art menu demands a Samsung account login that cannot be
dismissed** — art selection and Art Mode settings become unreachable from the remote. Samsung's own
`0090` release note foreshadowed this ("requires a Samsung account for Smart View").

**The local WebSocket API is not gated.** Art selection, art-mode settings and brightness all work
over the websocket with no account. This is the single strongest argument for a local controller:
it is not a convenience layer, it is the practical way to control this TV at all.

### Message envelope

Every art call is an `ms.channel.emit` on `com.samsung.art-app`:

```json
{"method":"ms.channel.emit",
 "params":{"event":"art_app_request","to":"host","data":"<JSON string of the request>"}}
```

Replies arrive as `d2d_service_message` whose `data` is itself a **JSON string** that must be parsed
again. Errors come back as `{"event":"error", "error_code":"-7", ...}`.

### Verified calls

| Request | Status | Notes |
|---|---|---|
| `get_artmode_status` | ✅ | `on`/`off` — the only reliable art-vs-normal signal |
| `set_artmode_status` | ✅ | `value: on`/`off`; emits `art_mode_changed` |
| `get_artmode_settings` | ✅ | full schema with ranges and valid values |
| `set_<item>` | ✅ | e.g. `{"request":"set_brightness","value":"9"}`; all five items confirmed (2026-11-01) |
| `get_content_list` | ✅ | all artworks; **ignores `category`** (2026-11-01, see "How content is stored") |
| `get_current_artwork` | ⚠️ | **unreliable — see below** |
| `select_image` | ⚠️ | **works only WITHOUT `category_id`** |
| `get_thumbnail_list` | ✅ | returns `conn_info` for a D2D socket fetch |
| `get_matte_list` | ✅ | matte types + colours |
| `get_photo_filter_list` | ✅ | `None, Aqua, ArtDeco, Ink, Wash, Pastel, Feuve` |
| `get_slideshow_status` | ✅ | |
| `get_device_info` | ✅ | sensor support, flash size |
| `set_artmode_settings` | ❌ | **silently does nothing** — no error, no effect |
| `get_api_version` | ❌ | never replies |

### Waking the panel — it depends which sleep it is

There are **two different standby states**, identical on every network signal (`PowerState:
standby`, 9197 refused, 8001/8002/1516 open, ICMP fine). The only field that tells them apart is
`get_artmode_status` — and they need **different wake methods**:

| Sleep state | `artmode_status` | What wakes it | What `KEY_POWER` does |
|---|---|---|---|
| Motion-sleep (timer fired in Art Mode) | **`on`** | **`select_image`** | **powers the TV fully OFF** ⚠ |
| Long-press power-off | **`off`** | **`KEY_POWER`** | wakes it (into Art Mode) |

`KEY_POWER` is a **toggle**, so its effect is the opposite in each state. Sending it blind is how you
lose the TV: from motion-sleep it powers down completely, and with IP Remote off that is
unrecoverable without the physical remote.

**Correct algorithm for "make the panel show art":**

```
read PowerState (REST) and artmode_status (art channel)

PowerState == "on"                      -> already awake; set_artmode_status on if needed
PowerState == "standby", artmode "on"   -> select_image <id>        (motion-sleep)
PowerState == "standby", artmode "off"  -> KEY_POWER                (powered off)
unreachable                             -> nothing can be done remotely
then: set_artmode_status on to ensure Art Mode rather than normal TV
```

Verified: `KEY_POWER` from the power-off state woke the TV straight into Art Mode
(`PowerState: on`, `artmode_status: on`, 9197 open). **The panel stays dark after that wake until
motion is detected; a `select_image` right after it lights the panel** (verified 2026-11-01). The
same holds whenever the TV is awake in Art Mode with a dark panel: `select_image` lights it, and
nothing the TV reports can tell that the panel was dark.

**`select_image` wakes the panel from motion-sleep. `set_artmode_status on` does not.**

Verified from motion-sleep (`PowerState: standby`, `artmode_status: on`):

```
set_artmode_status on   -> still standby    (no-op: art mode is already "on")
select_image <id>       -> PowerState "on"  (WAKES THE PANEL)
```

The working sequence for "show this art now" is therefore two steps:

1. `select_image` with the desired `content_id` (no `category_id`) — wakes the panel
2. `set_artmode_status on` — ensures it lands in Art Mode rather than normal TV

Step 2 matters: after waking, the TV may be in normal mode. `set_artmode_status` works normally once
awake and returns `{"status":"on"}`.

Remote-channel keys (`KEY_HOME`, `KEY_ENTER`, `KEY_UP`, `KEY_VOLUP`) also reach a sleeping TV, but
`KEY_HOME` lands on the home screen — use the art calls instead. **Never use `KEY_POWER` to wake:
from motion-sleep it powers the TV fully OFF**, after which it leaves the network entirely and only
the physical remote can recover it.

### Three gotchas that cost real time

1. **Setters are `set_<item>`, not `set_artmode_settings`.** `set_artmode_settings` accepts the
   request, returns no error, and changes nothing. All five setters are confirmed working
   (2026-11-01): `set_brightness`, `set_color_temperature`, `set_motion_sensitivity`,
   `set_motion_timer`, `set_brightness_sensor_setting`. Even so, an acknowledgement does not prove
   the value changed: re-read the settings.
2. **`select_image` must omit `category_id`.** Passing the `category_id` that `get_content_list`
   reports for that very item returns `error_code: -7`. Send only `content_id` and `show`.
3. **`get_current_artwork` reports intent, not the panel — and lags.** It is better understood as
   "the artwork the TV has selected", which the panel converges to **with a lag of minutes**. It has
   been observed both behind the panel (still reporting the old id after the art visibly changed)
   and ahead of it (reporting the new id while the deleted photo was still displayed, until the panel
   caught up). It is not lying — it is simply not a readout of what is currently illuminated.
   **The `select_image` reply (`is_shown: "Yes"`) and the `image_selected` event carry the real
   selection immediately; `get_current_artwork` was observed still reporting the previous artwork
   more than five minutes after a confirmed `select_image`, and reports the artworks it passes
   through on the way** (verified 2026-11-01). Track the selection from the reply and the event; use
   `get_current_artwork` only to notice changes made elsewhere. Combined with the fact that nothing
   reports panel illumination at all, **a client cannot truthfully claim "currently displaying X"**;
   it can only claim "currently selected X".

`error_code: -7` appears to be a generic refusal on this model — also reported for `change_matte`
on LS03D elsewhere.

### Art-mode settings schema

```
brightness                 4   (0-10)
color_temperature          0   (-5..5)
motion_sensitivity         2   (1-3)
motion_timer               5   valid: off, 5, 15, 30, 60, 120, 240   (minutes)
brightness_sensor_setting  on
```

`motion_timer` is what turns the panel off when the room is empty — the mechanism for night-off.
`brightness_sensor_setting: on` means ambient light also drives brightness, which may compete with a
manual `set_brightness`. Every item has a working `set_<item>` (see "Three gotchas").

### D2D socket transfers (thumbnails, uploads)

Bulk data does not travel on the websocket. The flow:

1. Send `get_thumbnail_list` (or `send_image`) including a `conn_info` block.
2. The TV replies with `conn_info` as a **JSON string** containing
   `{ip, port, key, secured}` — **`port` is a fresh random high port per request**
   (observed 51408, 54830, 58437).
3. Open a TCP connection to that ip/port, TLS when `secured` is true.
4. Read **4-byte big-endian length**, then that many bytes of JSON header
   (`fileName`, `fileType`, `fileLength`, `secKey`…), then `fileLength` bytes of payload.

**Wait before connecting** (verified 2026-11-01). The TV announces the port before it is ready to
serve it: connecting immediately makes it close the TLS handshake with EOF and then report
`error_code: -1`. Measured sequential thumbnail fetches: **4/10 succeeded with no delay, 16/16 each
with a 250ms, 500ms and 1s delay.** vigo waits 500ms.

**Some Art Store items have no thumbnail** (verified 2026-11-01). For them the TV opens the D2D
socket and closes it without sending any data. On this TV that was **22 of 106** Art Store items,
consistently per item, so it describes the item rather than the connection: remember it rather than
retrying.

**Verified working through the masquerade**: thumbnails pulled in under a second each, 320×180 JPEG,
~60 KB. This is the case that would have forced a SOCKS proxy under any port-forwarding design — a
masquerade on the TV's address handles arbitrary destination ports with no configuration.

### Upload — VERIFIED, and the one detail that breaks it

Uploading works, including on Tizen 9. The flow:

1. `send_image` with `conn_info` → TV replies `ready_to_use` carrying `conn_info`
2. Open the D2D socket (TLS if `secured`)
3. Send 4-byte BE header length, the JSON header, then the raw bytes
4. **Close the socket**
5. TV emits `send_image` then `image_added`, both carrying the new `content_id`

**The detail that silently breaks it: `conn_info.id` must be the SAME uuid as `id` and
`request_id`.** Use one `upload_id` for all three. Sending the client name there (or mismatched
uuids) makes the TV accept the request, hand back a port, accept the full byte transfer — and then
silently discard the image. No error, no `image_added`, nothing in the content list. Hours can
disappear into this.

```json
{"request":"send_image",
 "id":"<upload_id>", "request_id":"<upload_id>",
 "file_type":"jpg", "file_size":1531537,
 "image_date":"2026:11:01 06:39:13",
 "matte_id":"none", "portrait_matte_id":"shadowbox_polar",
 "conn_info":{"d2d_mode":"socket","connection_id":<random int>,"id":"<upload_id>"}}
```

Header on the D2D socket: `{"num":0,"total":1,"fileLength":<size>,"fileName":"image",
"fileType":"jpg","secKey":<conn_info.key>,"version":"0.0.1"}`.

Verified end to end through the masquerade: 1.5 MB JPEG, `image_added` in 5.6s, then
`select_image` → `is_shown: "Yes"`.

`image_added` carries no `request_id`, so a client cannot tell concurrent uploads apart: upload one
at a time.

### Image preparation — the TV does not fit, it crops

**The TV expects 3840x2160. Anything else gets cropped to fill, not letterboxed.** A portrait photo
uploaded as-is shows only a horizontal band of itself.

Two traps, both of which cost real time here:

1. **EXIF orientation.** A phone photo may have landscape raw pixels and an EXIF orientation tag
   (`274`) that rotates it. The sample was `3088x2320` raw with orientation `6` — genuinely a
   portrait photo. Upload the raw pixels and the TV gets it sideways.
2. **`sips` on macOS misreports dimensions** for such files — it claimed `3840x2160` for a file that
   was actually `2160x3840`. Do not trust it, and do not use it for this.

Prepare with an EXIF-aware library instead, server-side. With Pillow:

```python
im = ImageOps.exif_transpose(Image.open(src))      # bake rotation into pixels
im = im.convert("RGB")
out = ImageOps.pad(im, (3840, 2160), color=(0,0,0))   # pillarbox, keeps whole image
# or ImageOps.fit(...) to crop-to-fill instead
out.save(dst, "JPEG", quality=92)
```

Verified: the padded upload came back from the TV as `width: "3840", height: "2160"` and its
thumbnail shows the complete image with black bars; the un-padded one reported `2160x3840` and
displayed a cropped band.

**Offer both fits** — pad (whole image, bars) and crop (fills the screen, loses edges) — per image,
since which one is right is a taste decision. The `width`/`height` the TV reports back is a useful
post-upload check that the orientation survived.

### How content is stored

- **User uploads get `content_id` `MY_F0001`, `MY_F0002`, …** and `content_type: "mobile"`
  (Samsung's own art is `preinstall` or `server`).
- They land in category **`MY-C0002`** ("My Photos") and are *also* listed under `MY-C0008`.
- `image_date` is set from the upload request — it is the only field distinguishing user uploads
  from Samsung content, since every Samsung item has it empty.
- **`get_content_list` returns duplicates**: items belong to several categories, so an
  uncategorised listing repeats them. **Never count it to detect changes** — de-duplicate by
  `content_id`.
- **`get_content_list` ignores its `category` parameter on Tizen 9** and always lists everything
  (verified 2026-11-01). Filter client-side.
- Deletion exists: `delete_image_list` with `content_id_list`.
- Reported `width`/`height` came back **swapped** (2160×3840 for a 3840×2160 upload). Cosmetic, but
  do not trust those fields for orientation.

### Deleting the displayed artwork — the panel does not refresh

`delete_image_list` removes content cleanly (one `image_deleted` per id, plus a summary), and is
safe to batch. **But deleting the artwork currently on screen does not change the screen.** Observed:
after deleting `MY_F0003`, `get_current_artwork` moved to `SAM-F0222` while the panel kept showing
the deleted photo. There is no known `refresh` or `reload` request.

**UPDATE — the TV reconciles itself, with a lag.** Left alone after the delete, the panel did
eventually switch to `SAM-F0222` on its own, matching what `get_current_artwork` had reported
immediately. No command was sent. So a forced re-select is **not required for correctness**, only
for immediacy — the observed lag was minutes, and its upper bound is unknown.

**DECISION: do nothing.** A client does not need to force a selection after deleting the displayed
artwork. The TV handles it.

**`select_image` on the already-current artwork works** (verified 2026-11-01). An early attempt had
returned `error_code: -7`, but that call also wrongly included `category_id`, which was later proved
independently to cause -7.

### Storage limits — barely observable

The only storage figure the API exposes is `tv_flash_size: 16` (GB) from `get_device_info`, which is
whole-device flash, not an art quota. `get_storage_info`, `get_free_space` and `get_capacity` are
**not supported** — and note the TV **silently ignores unknown requests**: no error, no reply at
all. You cannot discover the API surface by probing for errors, and absence of a reply proves
nothing except that that exact name is wrong.

To track consumption, a client must keep its own tally of what it uploads.

### Content inventory (as of 2026-10-31)

135 artworks: 1 `preinstall`, 134 `server` (Art Store items pulled down during a brief internet
window), in categories `MY-C0008` (106) and `MY-C0004` (29), 3 flagged for slideshow, all 3840×2160.
No user-uploaded content at the time.

**Unverified:** whether the Art Store items keep rendering indefinitely now that the TV is offline
and unauthenticated, or whether they expire.
