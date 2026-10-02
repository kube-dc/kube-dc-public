package clusterinit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// An unset replica count follows the install topology. It never changes a
// running cluster: adopt/resume does not scaffold over an existing overlay.
func objectStorageReplication(mode RookMode, servers int, value string) (int, error) {
	if strings.TrimSpace(value) != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 3 {
			return 0, fmt.Errorf("CEPH_REPLICATION_SIZE must be 1, 2, or 3")
		}
		return n, nil
	}
	if mode == RookCephLocal || (mode == RookCephMultiNode && servers <= 1) {
		return 1, nil
	}
	return 2, nil
}

func (s ObjectStorageSpec) replication() int {
	if s.ReplicationSize != 0 {
		return s.ReplicationSize
	}
	n, _ := objectStorageReplication(s.Mode, len(s.CephNodes), "")
	return n
}

// Replace the template's three-slot list so unused nodes never become empty
// Rook selectors. Patch every pool and the global default to the same count.
func multiNodeStoragePatches(s ObjectStorageSpec) string {
	names := make([]string, 0, len(s.CephNodes))
	for name := range s.CephNodes {
		names = append(names, name)
	}
	sort.Strings(names)
	nodes := make([]map[string]any, 0, len(names))
	for i := range names {
		slot := strconv.Itoa(i + 1)
		nodes = append(nodes, map[string]any{"name": "${CEPH_NODE_" + slot + "}", "devices": []map[string]string{{"name": "${CEPH_NODE_" + slot + "_DEVICE}"}}})
	}
	mon, mgr, gateways := 3, 2, 2
	if len(names) == 1 {
		mon, mgr, gateways = 1, 1, 1
	}
	var b strings.Builder
	b.WriteString("patches:\n")
	patch := func(kind, name string, ops []map[string]any) {
		body, _ := json.MarshalIndent(ops, "      ", "  ")
		fmt.Fprintf(&b, "  - target:\n      group: ceph.rook.io\n      version: v1\n      kind: %s\n      name: %s\n      namespace: rook-ceph\n    patch: |\n      %s\n", kind, name, body)
	}
	set := func(path string, value any) map[string]any {
		return map[string]any{"op": "add", "path": path, "value": value}
	}
	patch("CephCluster", "rook-ceph", []map[string]any{
		set("/spec/storage/nodes", nodes),
		set("/spec/storage/useAllNodes", false), set("/spec/storage/useAllDevices", false),
		set("/spec/mon/count", mon), set("/spec/mon/allowMultiplePerNode", len(names) == 2),
		set("/spec/mgr/count", mgr),
		set("/spec/cephConfig", map[string]any{"global": map[string]string{"osd_pool_default_size": strconv.Itoa(s.replication())}}),
	})
	replicated := map[string]any{"size": s.replication(), "requireSafeReplicaSize": s.replication() > 1}
	patch("CephObjectStore", "my-store", []map[string]any{
		set("/spec/metadataPool/replicated", replicated), set("/spec/dataPool/replicated", replicated), set("/spec/gateway/instances", gateways),
	})
	patch("CephBlockPool", "rbd-pool", []map[string]any{set("/spec/replicated", replicated)})
	return b.String()
}
