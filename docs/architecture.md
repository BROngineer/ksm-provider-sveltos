# Architecture

How `ksm-provider-sveltos` works, and where each piece came from in kcm.

Related documents:

- [Operations](operations.md)
- [`ksm-provider-api` contract](https://github.com/brongineer/ksm-provider-api/blob/main/docs/api.md)
- [`ksm-provider-api` adapter guide](https://github.com/brongineer/ksm-provider-api/blob/main/docs/adapter-guide.md)

---

## 1. Components

| Component | Code | Runs on | Role |
|---|---|---|---|
| Provider API server | `cmd/main.go`, `providerserver.NewHandler` + `httpserver.Server` | every replica | serves kcm's `deployServices` / `deleteServices` calls; authenticates kcm with a TokenReview (audience `ksm-provider`) |
| `Adapter` | `internal/sveltos/provider.go` | every replica | implements the two operations on top of the moved kcm code |
| Heartbeater | `registryclient.Heartbeater` | leader | registers the provider in kcm's registry and keeps it alive; sends `Suspended` when the Deployment is scaled to zero |
| ClusterSummary poller | `internal/sveltos/enqueue.go`, `setup.go` | leader, after registration | detects Sveltos-side changes and emits the ServiceSets that need a reconcile |
| Notifier | `internal/sveltos/notifier.go` | leader | turns poller output into `ProviderStateChanged` Events (state-change notifications) |
| SveltosCluster controller | `internal/sveltos/cluster_controller.go` | leader, opt-in | renews tokens of the management SveltosCluster |

The adapter never writes ServiceSets. It reads them (with status) and writes
only Sveltos objects and Events.

## 2. Deploying a ServiceSet

`Adapter.DeployServices` runs what kcm's in-tree `ServiceSetReconciler.Reconcile`
did, up to and excluding the status write and the health verifier, which stay in
kcm:

| # | Step | Outcome |
|---|---|---|
| 1 | Get the ServiceSet | not found → `ErrNotFound` (404) |
| 2 | Compare generations | read < requested → `ErrStaleGeneration` (409); a newer read is used, and `observedGeneration` says which |
| 3 | Resolve the regional client | error → 500. kcm failed here too, before setting any condition |
| 4 | `ensureProfile` | builds and creates/updates the `Profile` / `ClusterProfile`; on failure → `applied: false` (section 3) |
| 5 | `collectServiceStatuses` | reads the `ClusterSummary`; on failure → `collected: false` with the condition message |
| 6 | Fingerprint | `fetchHelmArtifactsForVerifier` + `serviceHashesFromArtifacts` → `deployedHash` per service; best effort, a fetch error is only logged |
| 7 | Map services | `status.services` of the in-memory ServiceSet → `services[]` |

Steps 4 and 5 set the same conditions as before on the in-memory ServiceSet;
the result is derived from them, so the functions themselves are unchanged.

### 2.1 Where the regional client points

Sveltos objects live where the cluster's CAPI objects live:

| ServiceSet | Credential region | Client |
|---|---|---|
| self-managed (`spec.cluster` empty) | — | management cluster |
| for a ClusterDeployment | none | management cluster |
| for a ClusterDeployment | set | regional cluster, from the Region's kubeconfig Secret in the system namespace |

### 2.2 No matching cluster

When the Profile matches no cluster, collection succeeds without touching the
services (as in kcm). Their states may then be stale; kcm checks the target
cluster before calling and decides `deployed` itself.

## 3. From conditions to the result

| `ServiceSetProfile` condition reason | `applied` | `reason` |
|---|---|---|
| `ServiceSetProfileReady` | true | — |
| `ServiceSetProfileBuildFailed` (invalid `provider.config`) | false | `InvalidInput` |
| `ServiceSetHelmChartsBuildFailed`, `…KustomizationRefsBuildFailed`, `…PolicyRefsBuildFailed` | false | `SourceUnavailable` |
| `ServiceSetProfileNotReady` (ClusterDeployment, Credential or cluster lookup failed; Profile create/update failed) | false | `ProviderError` |

`message` is the condition message, which includes the underlying error. The
moved code does not tell "cannot fetch" from "invalid" for charts,
kustomizations and policies, so all three map to `SourceUnavailable`.

| `ServiceSetStatusesCollected` | `collected` | `collectMessage` |
|---|---|---|
| True | true | — |
| False | false | condition message (`Statuses not collected`) |

Per-service conditions are not sent: kcm keeps them and its verifier updates
them.

## 4. Deleting a ServiceSet

`Adapter.DeleteServices` is the Profile part of kcm's `reconcileDelete`. Marking
services `Deleting` and removing the finalizer stay in kcm.

| Profile / ClusterProfile | Action | `done` |
|---|---|---|
| exists, not being deleted | delete it | false |
| being deleted (finalizers pending) | wait | false |
| gone | — | true, on every later call too |

## 5. Telling kcm to reconcile

kcm cannot watch Sveltos objects, so the adapter notifies it. The poller is
kcm's `poller-cluster-summaries`, unchanged except for the provider filter:

| Situation (per ServiceSet of this provider) | Decision |
|---|---|
| `ClusterSummary` resource version changed | emit, reset back-off to 10s |
| fully deployed (`serviceset.FullyDeployed`) and unchanged | skip |
| in flight | emit with back-off 10s → 5m |
| other provider, being deleted, no Profile or ClusterSummary | skip |

Each emitted ServiceSet becomes a `ProviderStateChanged` Event through
`notify.StateChanged`.

**The poller starts only after the first accepted heartbeat** (`StartAfter` +
`Heartbeater.Registered()`). Its first tick marks every ServiceSet as changed;
emitted before registration, those notifications would be buffered, the buffer
would fill and drop events, and fully deployed ServiceSets would never be
notified again.

## 6. Replicas and leadership

- The provider API serves on every replica: kcm never reconciles the same
  ServiceSet concurrently.
- Heartbeater, poller, notifier and the SveltosCluster controller are
  leader-only.
- Leader election uses `LeaderElectionReleaseOnCancel`, so the `Suspended`
  heartbeat is sent before the lease is released.

## 7. Origin of the code

Everything below came from kcm commit `fd52a2d` (fork) and is unchanged unless
the "Change" column says otherwise.

| Here | kcm | Change |
|---|---|---|
| `internal/sveltos/profile.go` | `adapters/sveltos/serviceset_controller.go` (profile building, regional client) | receiver `ServiceSetReconciler` → `Adapter`; `record.Eventf` removed (kcm emits events); `schemeutil.GetRegionalSchemeWithSveltos` → `scheme.Regional` |
| `internal/sveltos/collect.go` | same file (status collection) | receiver; `record.Eventf` removed |
| `internal/sveltos/state.go` | `adapters/sveltos/state.go` | none |
| `internal/sveltos/fingerprint.go` | `adapters/sveltos/verify.go` (fingerprint part) | none |
| `internal/sveltos/enqueue.go` | `adapters/sveltos/enqueue.go` | `providerName` filter (replaces the StateManagementProvider selector) |
| `internal/sveltos/cluster_controller.go` | `adapters/sveltos/cluster_controller.go` | none |
| `internal/serviceset` | `internal/serviceset/util.go` | only `ServiceKey`, `DeployedVersions`, `FullyDeployed` |
| `internal/util/{kube,poller,ratelimit,pointer,helm}` | same paths | none |
| `internal/scheme` | `internal/util/scheme` | new: only the types the adapter reads |

New in this repository: `provider.go` (contract mapping), `notifier.go`
(`Notifier`, `StartAfter`), `setup.go`, `cmd/main.go`, the Dockerfile and the
chart.

**Stays in kcm:** the reconcile loop, status writes and events, the health
verifier and its rules, child-cluster clients, `fillNotDeployedServices`.

## 8. Testing

| Kind | Tests |
|---|---|
| Ported unchanged from kcm | `state_test.go`, fingerprint tests (`fingerprint_test.go`), `enqueue_test.go`, tests of the copied helpers |
| New, fake client | Profile building, collection, artifact fetch, provider operations, poller filter, notifier, `StartAfter`, cluster-controller helpers |
| Contract | `providertest.RunContract` against the `Adapter` (`TestContract`) |

The fake client is adjusted in tests to behave like a real API server for
cluster-scoped kinds (`newTestAdapter`). Not covered without a real cluster:
regional clients built from kubeconfig and SveltosCluster token renewal.
