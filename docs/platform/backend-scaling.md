# Running more than one backend API replica

The Kube-DC backend (`kube-dc-backend`, which also serves the console API) can
run several replicas behind the gateway. Each request may reach any replica:
one-use links and sessions work whichever one they land on, and a rolling
update replaces pods without a gap. One replica remains the default and needs
none of the settings below.

## What changes with more than one replica

| Concern | How replicas share it |
|---|---|
| VM console, SSH, object download and project-console links | Each link is a sealed, one-use ticket. Any replica opens it; the first redemption wins and a replay is refused. The replicas share the sealing key. |
| VM SSH and project-console relays | The relay connection a project Job opens back to the backend reaches any replica and is forwarded to the replica that holds the browser's session. |
| Org-admin elevations | Stored as ConfigMaps in the backend namespace; every replica sees a grant or a release within about two seconds. |
| Grafana launches, the Grafana login check and the query proxy | Run on one dedicated replica, `kube-dc-backend-grafana`, created automatically. |
| Paths that need a single copy (for example a ReadWriteOnce volume) | Listed in `backend.dedicatedReplica`; served by the dedicated replica for requests through the gateway (callers that use the backend Service directly reach the API replicas). |
| Rate limits | `backend.rateLimit` is the ceiling for the whole API; each replica gets its share. |
| Billing, reminder, notification and event workers | Run in the separate one-copy worker Deployment (`backend.worker.enabled`). |

A console session that is open while its replica is replaced ends; the user
opens it again (a new link is issued).

If the welcome-link store is unavailable when a WHMCS order completes, the
welcome email still goes out. With more than one replica, it carries the
console address, and the customer signs in through "Forgot password".

## Prerequisites

1. The worker Deployment: `backend.worker.enabled: true`.
2. A Secret in the backend namespace holding at least 32 random bytes under
   the key `key`, for example:

   ```bash
   kubectl -n kube-dc create secret generic kube-dc-backend-ticket-key \
     --from-literal=key="$(head -c 48 /dev/urandom | base64)"
   ```

   In a GitOps fleet, commit it encrypted (SOPS) instead.
3. If Grafana UID integration is enabled (`observabilityUID.grafanaEnabled`),
   the query path must follow the dedicated replica (next section).

## Settings

```yaml
backend:
  replicaCount: 2
  worker:
    enabled: true
  sealedTickets:
    keySecretName: kube-dc-backend-ticket-key
  rateLimit:            # whole-API ceilings per minute (defaults shown)
    apiPerMinute: 1200
    anonPerMinute: 300
  dedicatedReplica:     # optional: single-copy paths and their volumes
    paths: []
    volumes: []
    volumeMounts: []
```

The chart refuses more than one replica (or an HPA) without
`sealedTickets.keySecretName`, and without the worker Deployment. Under an HPA
each replica's share of the rate limit is computed from `maxReplicas`.

With Grafana UID integration, point the query proxy, the Grafana login check
and their certificate at the dedicated replica's Service. In the Kube-DC fleet
this is one variable in the cluster's `cluster-config.env`:

```
PAYG_QUERY_BACKEND_SERVICE=kube-dc-backend-grafana
```

The chart refuses an `observabilityUID.grafanaProxyURL` that does not name the
dedicated replica (`https://kube-dc-backend-grafana.<namespace>.svc:3335/...`).

The monitoring NetworkPolicies already admit pods labelled
`app.kubernetes.io/name: kube-dc-backend-grafana`. If your installation adds
its own policies or patches for the backend pods, include that name too.

## Checks after scaling

- `kubectl -n kube-dc get pods` shows the API replicas, one
  `kube-dc-backend-grafana` pod (when Grafana UID integration or
  `dedicatedReplica.paths` is used) and one worker.
- Open a VM console and a project console from the UI a few times; each opens
  once and works whichever replica served it.
- The backend log line `relay … forwarded to its owner` shows a relay that
  reached a replica other than the session's.

## Going back to one replica

Set `backend.replicaCount: 1` (the ticket key and dedicated-replica settings
may stay; with one replica the API pod serves everything again) and, with
Grafana UID integration, point `PAYG_QUERY_BACKEND_SERVICE` back to
`kube-dc-backend`.
