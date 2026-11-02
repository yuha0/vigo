# vigo

A self-hosted controller for the Samsung **The Frame** TV that works entirely on your local network, with no Samsung account and no cloud. It talks to the TV's local WebSocket API, serves a web app for it, and exposes it to Apple Home.

## No account, no cloud

On recent firmware Samsung gates the TV's own art settings and art selection behind a Samsung account: the on-screen Art menu demands a sign-in that cannot be dismissed. The TV's local API is not gated. vigo uses only that local API, so everything works without a Samsung account, without SmartThings and without internet access: you never need to sign in, not even once, and the TV can stay offline for good.

## Tested hardware

vigo has been tested on exactly one TV:

| | |
|---|---|
| Model | Samsung The Frame 2024, `QN75LS03DAFXZA` (75", US) |
| Firmware | `T-PTMDAKUC-0090-2130` (Tizen 9) |
| REST `model` | `24_PTM_FTV_T09` |

**No other model or firmware has been tested.** The TV's local API is undocumented and was reverse-engineered; other models and firmware versions may behave differently or not work at all, and a firmware update can break it. What was observed on the tested TV is recorded in [docs/tv-api.md](docs/tv-api.md).

vigo is not affiliated with or endorsed by Samsung or Apple.

## Features

- **Art Mode control without an account.** On recent firmware the TV's own Art menu demands a Samsung account sign-in; the local API does not. Turn Art Mode on and off, and show art on demand, waking the TV correctly from whichever standby state it is in.
- **Art settings** generated from what the TV reports: brightness, colour temperature, motion sensitivity, motion timer, brightness sensor. Every change is read back, and the UI says so when the TV acknowledges a change without applying it.
- **Art library.** One index of everything on the TV and in your own folders, with thumbnails, tags, filters and search. Art Store items are clearly marked as not managed by vigo.
- **Folder sync.** Point vigo at a directory of images and it keeps the TV in sync both ways: new files are uploaded, removed files are deleted from the TV, and art deleted on the TV is deleted from the folder. Images are oriented from EXIF and fitted to 3840×2160.
- **Editing.** Flags images with a low resolution or a ratio other than 16:9, and crops to 16:9 in the browser, saving back to the folder.
- **Uploads from the browser** into a synced folder.
- **Apple Home.** The TV as a HomeKit Television accessory with its own tile and the iOS Control Center remote, plus optional switches for Art Mode, "keep art on" and art brightness.
- **Virtual remote** in the web app.
- **Installable web app (PWA)** for phones.
- **Built to run highly available.** Multiple replicas with leader election on PostgreSQL, mutual TLS between components, secrets encrypted at rest, Prometheus metrics and structured logs.

## Components

| Component | Image | Role |
|---|---|---|
| vigo-core | `ghcr.io/yuha0/vigo-core` | Go service: the TV connection, the API, the web app, the database |
| vigo-home | `ghcr.io/yuha0/vigo-home` | Node service: the Apple Home (HomeKit) accessories |

Images are published for `linux/amd64` and `linux/arm64`, tagged `main` and `sha-<commit>` for every commit on `main`, and `v-<version>` for releases.

## Requirements

- **The TV must see vigo-core's traffic as coming from its own subnet;** it refuses WebSocket clients from anywhere else. Run vigo-core on that subnet, or have your router masquerade traffic to the TV ([docs/tv-api.md](docs/tv-api.md) §1).
- On the TV: **IP Remote** on (otherwise it leaves the network in standby), and Device Connection Manager › Access Notification set to **First Time Only**. Pairing needs someone at the TV to press Allow once.
- PostgreSQL 18.
- An X.509 CA for mutual TLS between vigo-core and vigo-home, for example cert-manager.
- For Apple Home: vigo-home on the same LAN as your Apple devices (it advertises over mDNS).

## Deploying

[deploy/](deploy/) holds a sample Kubernetes deployment with instructions in [deploy/README.md](deploy/README.md). Adapt it to your cluster.

## Documentation

- [docs/design.md](docs/design.md): architecture, API contract, storage, security, sync rules.
- [docs/tv-api.md](docs/tv-api.md): the TV's local API as observed on the tested TV.

## Development

See [docs/design.md § Development](docs/design.md#development). The API contract lives in `proto/`; run `make gen` after changing it.

## License

[MIT](LICENSE)
