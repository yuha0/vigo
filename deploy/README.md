# Deploying vigo on Kubernetes

`deploy/kustomize/` is a sample: everything runs in the `vigo` namespace, and a few values must be adapted to your cluster (marked in the manifests). It deploys:

| Directory | Contents |
|---|---|
| `system/` | the namespace, the system CA (cert-manager `Issuer`s and CA `Certificate`), the PostgreSQL cluster (CloudNativePG) and its NetworkPolicy |
| `core/` | vigo-core: Deployment, Services, mTLS certificate, art volume, HTTPRoute, ServiceMonitor |
| `home/` | vigo-home (Apple Home): Deployment, mTLS certificate, metrics Service, ServiceMonitor |

Images are `ghcr.io/yuha0/vigo-core` and `ghcr.io/yuha0/vigo-home`. The sample uses the `main` tag; pin a released tag or digest for anything you rely on.

## Prerequisites

- [cert-manager](https://cert-manager.io): issues the internal mTLS certificates.
- [CloudNativePG](https://cloudnative-pg.io): runs PostgreSQL. The NetworkPolicy assumes the operator runs in `cnpg-system`.
- A StorageClass that provides **ReadWriteMany** volumes for the art library (`core/volume.yaml`), and one that provides ReadWriteOnce volumes for PostgreSQL. Both manifests use the cluster default class; set `storageClassName` / `storage.storageClass` to choose others.
- A Gateway API implementation for the HTTPRoute (`core/httproute.yaml`), or replace it with an Ingress.
- Optional: the Prometheus Operator, for the ServiceMonitors. Remove them if you don't run it.

## Network

- **The TV must see vigo-core's traffic as coming from its own subnet.** The TV refuses WebSocket connections from other subnets. Either route the pod network to the TV through a gateway that masquerades (source-NATs) that traffic into the TV's subnet, or run vigo-core on a node in that subnet with addresses the TV accepts. Set the TV's address in `VIGO_TV_HOST` (`core/deployment.yaml`).
- **vigo-home uses `hostNetwork`**, because HomeKit advertises over link-local mDNS. It needs the node to be on the same LAN as your Apple devices, and the HAP ports 51830 and 51831 free on the node. Its required anti-affinity on `app.kubernetes.io/component=mdns` keeps it off nodes running other mDNS/HAP advertisers; label those pods the same way.
- Don't expose vigo-core's HTTP port (8080) publicly: the web UI has no authentication.

## The encryption keyring

vigo-core encrypts the TV token and Apple Home pairing state before they reach the database, with keys from a keyring file. The keyring is **not** in the database or in this sample: create it yourself, keep a copy outside the cluster (a password manager, for example), and never lose it. A database backup cannot be read without it.

```sh
vigo-core keyring new --out keyring.json      # or: go run ./cmd/vigo-core keyring new --out keyring.json
kubectl create namespace vigo
kubectl -n vigo create secret generic vigo-keyring --from-file=keyring.json
```

If you manage the cluster with GitOps, commit it as an encrypted secret (Sealed Secrets, SOPS, External Secrets, ...) that produces a Secret named `vigo-keyring` with the key `keyring.json`.

To rotate, run `vigo-core keyring rotate --file keyring.json --id <new-id>`, update the Secret and restart vigo-core. Old keys stay in the file so existing values still decrypt.

## Apply order

1. Install the prerequisites, and create the `vigo-keyring` Secret.
2. Adapt the marked values: `VIGO_TV_HOST`, the HTTPRoute, storage classes, the CloudNativePG operator namespace in `system/db/network-policy.yaml`.
3. Render and apply:
   ```sh
   kubectl kustomize deploy/kustomize      # review
   kubectl apply -k deploy/kustomize
   ```
   CloudNativePG needs a minute or two to bootstrap the database; vigo-core pods may restart until it is ready (`kubectl -n vigo get cluster db`).
4. If the art volume is a CIFS/SMB share, patch its PersistentVolume as described in `core/volume.yaml`, then restart vigo-core.

## Pairing with the TV

Pair from the web UI (TV page): accept the prompt on the TV. If you already have a token for this TV, for example from another client, import it instead of pairing again:

```sh
kubectl -n vigo exec -i deploy/vigo-core -- /vigo-core import-token --file /dev/stdin < token.json
kubectl -n vigo rollout restart deploy/vigo-core
```

The file holds the raw token or a JSON object with a `token` field (and optionally `name`, the client name it was paired under; pass `--client-name` otherwise).

## Apple Home

Open Integrations › Apple Home in the web UI, then add the accessories with the Home app by scanning the QR codes. vigo-home keeps no state of its own: pairings live in vigo-core's database.
