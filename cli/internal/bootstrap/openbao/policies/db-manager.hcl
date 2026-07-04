# db-manager (M3-T05) — wraps + unwraps backup DEKs against per-Org
# Transit keys. Tight scope: read key metadata + encrypt + decrypt
# only. db-manager NEVER mounts, rotates, or destroys keys — those
# are kube-dc-controller-manager's surface.

# Per-Org Transit grants. The `+` is OpenBao policy's single-segment
# glob — matches any child namespace (== Organization), since the
# kube-dc KMSKey reconciler stamps status.keyId as
# "<org>/transit/keys/<name>".
path "+/transit/keys/+"      { capabilities = ["read"] }
path "+/transit/encrypt/+"   { capabilities = ["update"] }
path "+/transit/decrypt/+"   { capabilities = ["update"] }

# M4-T01: db-manager registers each KdcDatabase as
# "<org>/database/config/<dbname>". Owns the per-Org database mount
# (sys/mounts/database) and the config entries underneath, but NEVER
# writes roles — kube-dc-controller-manager owns
# "<org>/database/roles/<dbname>-<role>" via DatabaseCredentialPolicy.
# `sudo` on sys/mounts is required to enable the secrets engine.
path "+/sys/mounts/database"   { capabilities = ["create","read","update","sudo"] }
path "+/database/config"       { capabilities = ["list"] }
path "+/database/config/+"     { capabilities = ["create","read","update","delete"] }
# Reset the connection after credential rotation so OpenBao re-handshakes.
path "+/database/reset/+"      { capabilities = ["update"] }

# Token self-management for the controller's lease renewal loop.
path "auth/token/lookup-self" { capabilities = ["read"] }
path "auth/token/renew-self"  { capabilities = ["update"] }
