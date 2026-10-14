# ksm-provider-sveltos

The [ProjectSveltos](https://projectsveltos.github.io/sveltos/) state management
provider for [k0rdent](https://github.com/K0rdent/kcm).

kcm decides **what** services a cluster should run and records it in ServiceSets.
This adapter makes Sveltos deploy them: for each ServiceSet kcm asks about, it
creates or updates a Sveltos `Profile` (or `ClusterProfile` for the management
cluster), reads back what Sveltos deployed and reports it to kcm in the format
defined by [`ksm-provider-api`](https://github.com/brongineer/ksm-provider-api).

The code was moved out of kcm (`internal/controller/adapters/sveltos`) without
changing its logic; see [Architecture](docs/architecture.md) for what moved
where and what changed.

```text
                 heartbeats (registry API)
 ksm-provider-sveltos ─────────────────────────▶ kcm registry
        ▲                                             │
        │ deploy / delete ServiceSet (provider API)   │
        └──────────────────────────────────────── kcm ServiceSet reconciler
                                                      ▲
 ksm-provider-sveltos ── Event ProviderStateChanged ──┘

 ksm-provider-sveltos ── Profile / ClusterProfile ──▶ Sveltos ──▶ target clusters
```

## Documentation

| Document | Content |
|---|---|
| [Architecture](docs/architecture.md) | components, request flow, how Sveltos state becomes a contract result, the poller, origin of every piece of code |
| [Operations](docs/operations.md) | installing, configuration reference, registering with kcm, suspending, troubleshooting |
| [API contract](https://github.com/brongineer/ksm-provider-api/blob/main/docs/api.md) | the provider and registry APIs this adapter implements |

## Requirements

- kcm with the provider registry, and a StateManagementProvider named like the
  provider (`ksm-projectsveltos` by default) declaring this adapter's
  ServiceAccount; the chart renders it with `stateManagementProvider.create`.
- ProjectSveltos (addon-controller) v1.16.x, the version kcm ships.
- cert-manager, only when serving TLS (`tls.insecure: false`) with the
  chart-issued certificate.

## Quick start

kcm ships this adapter as part of its `projectsveltos` component and configures
it; a standalone install is only needed outside kcm's components.

The chart is published to `oci://ghcr.io/brongineer/charts/ksm-provider-sveltos`
on every release tag. By default it runs in insecure mode, matching kcm's
default registry (plain HTTP on both sides; **never in production**, see
[Operations](docs/operations.md#insecure-mode-default)):

```sh
helm install ksm-provider-sveltos oci://ghcr.io/brongineer/charts/ksm-provider-sveltos \
  --namespace projectsveltos
```

With TLS on both sides:

```sh
helm install ksm-provider-sveltos oci://ghcr.io/brongineer/charts/ksm-provider-sveltos \
  --namespace projectsveltos \
  --set tls.insecure=false \
  --set registry.url=https://kcm-ksm-registry.kcm-system.svc:8443 \
  --set registry.caSecret.name=kcm-ksm-registry-ca
```

Then allow the adapter in kcm (see [Operations](docs/operations.md#registering-with-kcm)).

## Development

Tools are pinned with [mise](https://mise.jdx.dev).

| Task | Does |
|---|---|
| `mise run lint` | `go fmt`, `go vet`, `go fix`, `golangci-lint` |
| `mise run test` | `go test -race ./... -cover` |
| `mise run build` | `go build ./...` |
| `mise run tidy` | `go mod tidy` |

Tests use plain `testing`, table tests and controller-runtime's fake client; no
API server is needed. Paths that need a real cluster (regional clients from
kubeconfig, SveltosCluster token renewal) are left to integration tests.

The module builds against the kcm fork pinned in `go.mod`
(`replace github.com/K0rdent/kcm => github.com/BROngineer/kcm …`).
