# Hourly DR snapshots need only the read form of this endpoint.
# Restore (update) and snapshot-force are deliberately not granted.
path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}

# No default policy: the role issues a short-lived, non-renewed token.
