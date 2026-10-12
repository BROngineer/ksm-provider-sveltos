# Operations

Installing, configuring and troubleshooting `ksm-provider-sveltos`.

Related documents:

- [Architecture](architecture.md)
- [`ksm-provider-api` contract](https://github.com/brongineer/ksm-provider-api/blob/main/docs/api.md)

---

## Installing

```sh
helm install ksm-provider-sveltos charts/ksm-provider-sveltos \
  --namespace projectsveltos \
  --set registry.url=https://kcm-ksm-registry.kcm-system.svc:8443 \
  --set registry.caSecret.name=kcm-ksm-registry-ca
```

The chart creates:

| Object | Purpose |
|---|---|
| Deployment | the adapter; runs as non-root with a read-only root filesystem |
| Service | the provider API endpoint kcm calls (`https://<release>.<namespace>.svc:9443`) |
| Issuer + Certificate | self-signed serving certificate via cert-manager (`tls.certManager: true`) |
| ServiceAccount, ClusterRole, ClusterRoleBinding | cluster-wide reads, Sveltos objects, Events, TokenReviews |
| Role, RoleBinding | leader-election leases and `get` on the adapter's own Deployment |

The endpoint and its CA (`ca.crt` of the serving Secret) are announced to kcm in
every heartbeat; nothing has to be configured in kcm for them.

## Registering with kcm

kcm accepts heartbeats only from ServiceAccounts on its allow-list. Add the
provider name with this release's ServiceAccount to kcm's configuration:

```yaml
ksm:
  providers:
    - name: ksm-projectsveltos
      serviceAccount: projectsveltos/ksm-provider-sveltos
```

`name` must equal the chart's `providerName` and the
`spec.provider.name` of the ServiceSets this adapter should serve.

## Configuration

| Value | Flag | Default | Meaning |
|---|---|---|---|
| `providerName` | `--provider-name` | `ksm-projectsveltos` | name registered in kcm and matched against `ServiceSet.spec.provider.name` |
| `systemNamespace` | `--system-namespace` | `kcm-system` | kcm system namespace; Region kubeconfig Secrets are read from there |
| `kcmServiceAccount` | `--kcm-service-account` | `kcm-system/kcm-controller-manager` | the only caller allowed on the provider API |
| `registry.url` | `--registry-url` | `https://kcm-ksm-registry.kcm-system.svc:8443` | kcm registry base URL; required |
| `registry.caSecret.name` / `.key` | `--registry-ca-file` | empty / `ca.crt` | Secret with the CA verifying the registry; empty uses system roots |
| `heartbeatInterval` | `--heartbeat-interval` | `10s` | at least 1s; registrations live 3 × this |
| `providerTimeout` | `--provider-timeout` | empty | per-call timeout announced to kcm; empty leaves the registry default (30s) |
| `leaderElect` | `--leader-elect` | `true` | leader election for heartbeater, poller and controllers |
| `enableSveltosExpireCtrl` | `--enable-sveltos-expire-ctrl` | `false` | renew tokens of the management SveltosCluster (adds the RBAC it needs) |
| `tls.certManager` / `tls.secretName` | `--tls-cert-file`, `--tls-key-file`, `--endpoint-ca-file` | `true` / empty | serving certificate; without cert-manager provide a Secret with `tls.crt`, `tls.key`, `ca.crt` |
| `service.port` | — | `9443` | Service port of the provider API |
| `logLevel` | `--zap-log-level` | `info` | log level |
| `image.*`, `resources`, `nodeSelector`, `tolerations`, `affinity` | — | | usual Deployment settings |

Fixed by the chart: `--endpoint` (the Service URL), `--deployment-name` (the
release name), `--pod-namespace` (from the Downward API), the projected registry
token at `/var/run/secrets/ksm/registry/token` with audience `ksm-registry`.

## Suspending the provider

Scale the Deployment to zero:

```sh
kubectl -n projectsveltos scale deployment ksm-provider-sveltos --replicas=0
```

On shutdown the leader checks its Deployment, sees `replicas: 0` and sends a
final `Suspended` heartbeat; kcm stops calling the adapter and reports its
ServiceSets as suspended. Scaling back up registers it as `Active` again.

Rollouts, evictions and node drains do not suspend the provider: the
registration simply expires if no replica heartbeats for 3 × `heartbeatInterval`.

## Troubleshooting

| Symptom | Check |
|---|---|
| ServiceSets stay unknown in kcm | Adapter logs show `heartbeat failed`? The message carries the registry's status and problem type: `403 forbidden` → allow-list entry missing or wrong ServiceAccount; `401 unauthenticated` → projected token audience is not `ksm-registry`; `503 not-leader` → transient while kcm elects a leader; TLS errors → `registry.caSecret` |
| kcm cannot call the adapter | `401`/`403` on the adapter side: kcm's token audience must be `ksm-provider`, and kcm must run as `kcmServiceAccount`. TLS errors: the CA announced in heartbeats is the serving Secret's `ca.crt` |
| ServiceSet never updates after deployment progresses | `kubectl get events --field-selector reason=ProviderStateChanged -n <ns>`: no Events → the poller is not running (not leader, or not registered yet; it starts only after the first accepted heartbeat) |
| `applied: false` with `SourceUnavailable` | the ServiceTemplate, its HelmChart/HelmRepository/GitRepository, or a Kustomize/Resources source is missing or invalid; `message` names it |
| `applied: false` with `ProviderError` | ClusterDeployment, Credential or the target CAPI Cluster/SveltosCluster is missing, or the Profile could not be written; see adapter logs `failed to ensure Profile` |
| `collected: false` | the Profile matches a cluster but its ClusterSummary is missing; Sveltos has not processed the Profile yet or failed — check addon-controller logs |
| Regional ServiceSets fail with 500 | the Region's kubeconfig Secret in `systemNamespace` is missing or unreachable |

Metrics are served on `:8080`, health and readiness probes on `:8081`
(`/healthz`, `/readyz`).
