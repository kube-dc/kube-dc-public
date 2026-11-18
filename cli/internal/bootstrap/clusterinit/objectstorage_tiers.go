package clusterinit

// Storage tiers for rook-ceph-pvc. The shared mode has one OSD device set and
// replicated pools without a device class, so every pool lands on every OSD.
// A site with two kinds of SAN storage (flash for block and bucket indexes,
// slow capacity storage for bucket data) needs a second device set, explicit
// CRUSH device classes, pools pinned to them and, for large buckets, an
// erasure-coded data pool. Erasure coding cannot be added to an existing pool,
// so it has to be chosen before the first reconcile.
//
// The settings are optional cluster-config keys, read from o.Sets like
// CEPH_MON_NODES. Without them the generated overlay is unchanged. With them
// the overlay gains JSON patches; the shared Fleet mode is not modified.

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	k8syaml "sigs.k8s.io/yaml"
)

const (
	KeyCephOSDDeviceClass          = "CEPH_OSD_DEVICE_CLASS"
	KeyCephCapacityOSDStorageClass = "CEPH_CAPACITY_OSD_STORAGE_CLASS"
	KeyCephCapacityOSDCount        = "CEPH_CAPACITY_OSD_COUNT"
	KeyCephCapacityOSDVolumeSizeGB = "CEPH_CAPACITY_OSD_VOLUME_SIZE_GB"
	KeyCephCapacityOSDDeviceClass  = "CEPH_CAPACITY_OSD_DEVICE_CLASS"
	KeyCephCapacityOSDPortable     = "CEPH_CAPACITY_OSD_PORTABLE"
	KeyCephBlockPoolDeviceClass    = "CEPH_BLOCK_POOL_DEVICE_CLASS"
	KeyCephRGWMetadataDeviceClass  = "CEPH_RGW_METADATA_DEVICE_CLASS"
	KeyCephRGWDataDeviceClass      = "CEPH_RGW_DATA_DEVICE_CLASS"
	KeyCephRGWDataErasureCoding    = "CEPH_RGW_DATA_EC"
	defaultCephCapacityDeviceClass = "hdd"
	cephPVCPrimaryDeviceSet        = "osd-template"
	cephPVCCapacityDeviceSet       = "osd-capacity"
	cephPVCFleetDefaultOSDCount    = 2   // modes/rook-ceph-pvc: ${CEPH_OSD_COUNT:=2}
	cephPVCFleetDefaultOSDSizeGB   = 200 // modes/rook-ceph-pvc: ${CEPH_OSD_VOLUME_SIZE_GB:=200}Gi
	// Rook reads an OSD's CRUSH class for a PVC-backed device set from this
	// annotation on the data PVC template (pkg/operator/ceph/cluster/osd/deviceSet.go),
	// not from a field on the device set.
	cephCrushDeviceClassAnnotation  = "crushDeviceClass"
	cephPVCDataClaimTemplate        = "data"
	cephErasureCodingMaxChunkTotal  = 16
	cephErasureCodingMinDataChunks  = 2
	cephErasureCodingMinCodingChunk = 1
)

// cephTierKeys lists every tier key, for the "only for rook-ceph-pvc" check.
var cephTierKeys = []string{
	KeyCephOSDDeviceClass, KeyCephCapacityOSDStorageClass, KeyCephCapacityOSDCount,
	KeyCephCapacityOSDVolumeSizeGB, KeyCephCapacityOSDDeviceClass, KeyCephCapacityOSDPortable,
	KeyCephBlockPoolDeviceClass, KeyCephRGWMetadataDeviceClass, KeyCephRGWDataDeviceClass,
	KeyCephRGWDataErasureCoding,
}

// A count or size that reaches Rook through Flux substitution is re-read as
// YAML 1.1, where 010 is octal 8 and +10 or 1e1 are not the decimal the CLI
// validated. Accept only the canonical decimal form.
var canonicalPositiveInt = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

// yamlReadsAsString reports whether a value substituted unquoted into a
// manifest stays the same string when Flux decodes it (sigs.k8s.io/yaml, YAML
// 1.1). Kustomize drops the quotes around a whole-value "${VAR}", so a
// StorageClass named 123, 1e3, 0x10, yes or null would reach Rook as a number,
// a boolean or null.
func yamlReadsAsString(v string) bool {
	var doc map[string]any
	if err := k8syaml.Unmarshal([]byte("v: "+v), &doc); err != nil {
		return false
	}
	s, ok := doc["v"].(string)
	return ok && s == v
}

// validatePVCPrimaryKeys covers the template set's own keys where they reach
// the manifest unvalidated: a --set CEPH_OSD_COUNT / CEPH_OSD_VOLUME_SIZE_GB is
// written to cluster-config.env as given unless the dedicated flag overrides
// it (an explicit 0 would be substituted as 0; Rook requires count >= 1), and
// the StorageClass is substituted unquoted.
func validatePVCPrimaryKeys(o *InitOptions) []string {
	var errs []string
	for _, k := range []struct {
		key, flag string
		value     int
	}{{"CEPH_OSD_COUNT", "--ceph-osd-count", o.CephOSDCount}, {"CEPH_OSD_VOLUME_SIZE_GB", "--ceph-osd-volume-size-gb", o.CephOSDVolumeSizeGB}} {
		v, set := o.Sets[k.key]
		if !set || k.value > 0 {
			continue // the flag's value replaces it in cluster-config.env
		}
		if v = strings.TrimSpace(v); !canonicalPositiveInt.MatchString(v) {
			errs = append(errs, fmt.Sprintf("--set %s=%q: use a decimal whole number >= 1 without leading zeros, or omit it for the Fleet default (it is substituted as written, and YAML reads 0 or 010 differently)", k.key, v))
		}
	}
	if o.CephStorageClass != "" && !yamlReadsAsString(o.CephStorageClass) {
		errs = append(errs, fmt.Sprintf("--ceph-storage-class %q would reach Rook as a %s, not a string, after Flux substitution; use a name that is not a YAML number, boolean or null", o.CephStorageClass, yamlKind(o.CephStorageClass)))
	}
	return errs
}

func yamlKind(v string) string {
	var doc map[string]any
	if k8syaml.Unmarshal([]byte("v: "+v), &doc) != nil {
		return "parse error"
	}
	switch doc["v"].(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	}
	return "different string"
}

// Ceph device class names: short, lowercase, as `ceph osd crush class ls` shows them.
var cephDeviceClassRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// CephTiers is the parsed form of the tier keys.
type CephTiers struct {
	OSDDeviceClass string // crushDeviceClass of the template's device set

	CapacityStorageClass string // enables the second device set
	CapacityOSDCount     int
	CapacityOSDSizeGB    int
	CapacityDeviceClass  string
	CapacityPortable     bool

	BlockPoolDeviceClass   string // rbd-pool (ceph-block)
	RGWMetadataDeviceClass string // my-store metadata pool (index, meta, log)
	RGWDataDeviceClass     string // my-store data pool

	// RGWDataEC is K+M for an erasure-coded data pool; zero keeps it replicated.
	RGWDataECDataChunks   int
	RGWDataECCodingChunks int
}

func (t CephTiers) active() bool { return t != CephTiers{} }

func (t CephTiers) erasureCoded() bool { return t.RGWDataECDataChunks > 0 }

// parseCephTiers reads the tier keys. It never fails: malformed values become
// zero here and are reported by validateCephTiers.
func parseCephTiers(sets map[string]string) CephTiers {
	get := func(k string) string { return strings.TrimSpace(sets[k]) }
	atoi := func(k string) int {
		n, err := strconv.Atoi(get(k))
		if err != nil {
			return 0
		}
		return n
	}
	t := CephTiers{
		OSDDeviceClass:         get(KeyCephOSDDeviceClass),
		CapacityStorageClass:   get(KeyCephCapacityOSDStorageClass),
		CapacityOSDCount:       atoi(KeyCephCapacityOSDCount),
		CapacityOSDSizeGB:      atoi(KeyCephCapacityOSDVolumeSizeGB),
		CapacityDeviceClass:    get(KeyCephCapacityOSDDeviceClass),
		CapacityPortable:       parsePrefillBool(get(KeyCephCapacityOSDPortable)),
		BlockPoolDeviceClass:   get(KeyCephBlockPoolDeviceClass),
		RGWMetadataDeviceClass: get(KeyCephRGWMetadataDeviceClass),
		RGWDataDeviceClass:     get(KeyCephRGWDataDeviceClass),
	}
	if t.CapacityStorageClass != "" && t.CapacityDeviceClass == "" {
		t.CapacityDeviceClass = defaultCephCapacityDeviceClass
	}
	if k, m, ok := parseErasureCoding(get(KeyCephRGWDataErasureCoding)); ok {
		t.RGWDataECDataChunks, t.RGWDataECCodingChunks = k, m
	}
	return t
}

// parseErasureCoding accepts "K+M", for example "4+2".
func parseErasureCoding(v string) (k, m int, ok bool) {
	parts := strings.Split(v, "+")
	if len(parts) != 2 {
		return 0, 0, false
	}
	k, errK := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, errM := strconv.Atoi(strings.TrimSpace(parts[1]))
	// k > max-m rather than k+m > max: with the lower bounds checked first,
	// max-m cannot overflow, while k+m wraps for a K near the int maximum.
	if errK != nil || errM != nil || k < cephErasureCodingMinDataChunks || m < cephErasureCodingMinCodingChunk ||
		k > cephErasureCodingMaxChunkTotal-m {
		return 0, 0, false
	}
	return k, m, true
}

// pvcPrimaryOSDs resolves the template set's OSD count and size with the
// scaffold's precedence: postProcessClusterConfig writes o.Sets first, then
// WriteObjectStorage writes CEPH_OSD_COUNT / CEPH_OSD_VOLUME_SIZE_GB from the
// dedicated flags when they are > 0, and the Fleet template defaults the rest.
func pvcPrimaryOSDs(o *InitOptions) (count, sizeGB int) {
	pick := func(flag int, key string, def int) int {
		if flag > 0 {
			return flag
		}
		if n, err := strconv.Atoi(strings.TrimSpace(o.Sets[key])); err == nil && n > 0 {
			return n
		}
		return def
	}
	return pick(o.CephOSDCount, "CEPH_OSD_COUNT", cephPVCFleetDefaultOSDCount),
		pick(o.CephOSDVolumeSizeGB, "CEPH_OSD_VOLUME_SIZE_GB", cephPVCFleetDefaultOSDSizeGB)
}

// pvcReplicas is the size of every replicated pool the mode ships
// (rbd-pool and both object-store pools read ${CEPH_REPLICATION_SIZE:=2}).
func pvcReplicas(o *InitOptions) (int, error) {
	return objectStorageReplication(RookCephPVC, 0, o.Sets["CEPH_REPLICATION_SIZE"])
}

// pvcBlockPoolRawGB is the raw capacity of the OSDs rbd-pool (ceph-block) can
// use: the set whose class it is pinned to, or every set when it is not.
func pvcBlockPoolRawGB(o *InitOptions) (int64, error) {
	t := parseCephTiers(o.Sets)
	count, size := pvcPrimaryOSDs(o)
	mul := func(a, b int) (int64, error) {
		if a < 0 || b < 0 || (b > 0 && int64(a) > math.MaxInt64/int64(b)) {
			return 0, fmt.Errorf("managed-services PVC capacity exceeds supported range")
		}
		return int64(a) * int64(b), nil
	}
	primary, err := mul(count, size)
	if err != nil {
		return 0, err
	}
	var capacity int64
	if t.CapacityStorageClass != "" {
		if capacity, err = mul(t.CapacityOSDCount, t.CapacityOSDSizeGB); err != nil {
			return 0, err
		}
	}
	switch dc := t.BlockPoolDeviceClass; {
	case dc == "":
		if primary > math.MaxInt64-capacity {
			return 0, fmt.Errorf("managed-services PVC capacity exceeds supported range")
		}
		return primary + capacity, nil
	case dc == t.OSDDeviceClass:
		return primary, nil
	case t.CapacityStorageClass != "" && dc == t.CapacityDeviceClass:
		return capacity, nil
	}
	return 0, nil // no set provides the class; validateCephTiers reports it
}

// validateCephTiers checks the tier keys against the selected mode and the
// OSD counts. Pools use failureDomain host, so a class must hold at least as
// many OSDs as a pool needs hosts; the OSDs of a set must also sit on distinct
// nodes (static PVs pin them, CSI-backed sets rely on the anti-affinity).
func validateCephTiers(o *InitOptions) []string {
	set := func(k string) bool { return strings.TrimSpace(o.Sets[k]) != "" }
	var used []string
	for _, k := range cephTierKeys {
		if set(k) {
			used = append(used, k)
		}
	}
	if len(used) == 0 {
		return nil
	}
	if o.RookMode != RookCephPVC {
		return []string{fmt.Sprintf("%s apply only to --object-storage-mode=rook-ceph-pvc", strings.Join(used, ", "))}
	}
	t := parseCephTiers(o.Sets)
	var errs []string
	class := func(k, v string) {
		if v != "" && !cephDeviceClassRegex.MatchString(v) {
			errs = append(errs, fmt.Sprintf("%s %q is not a valid Ceph device class (lowercase a-z, 0-9, '-', '_', at most 32 characters)", k, v))
		}
	}
	class(KeyCephOSDDeviceClass, t.OSDDeviceClass)
	class(KeyCephCapacityOSDDeviceClass, t.CapacityDeviceClass)
	class(KeyCephBlockPoolDeviceClass, t.BlockPoolDeviceClass)
	class(KeyCephRGWMetadataDeviceClass, t.RGWMetadataDeviceClass)
	class(KeyCephRGWDataDeviceClass, t.RGWDataDeviceClass)

	// The capacity device set.
	if t.CapacityStorageClass == "" {
		for _, k := range []string{KeyCephCapacityOSDCount, KeyCephCapacityOSDVolumeSizeGB, KeyCephCapacityOSDDeviceClass, KeyCephCapacityOSDPortable} {
			if set(k) {
				errs = append(errs, fmt.Sprintf("%s requires %s", k, KeyCephCapacityOSDStorageClass))
			}
		}
	} else {
		if !validStorageClassName(t.CapacityStorageClass) {
			errs = append(errs, fmt.Sprintf("%s %q is not a valid StorageClass name (a DNS subdomain: lowercase labels of a-z, 0-9 and '-', separated by single dots)", KeyCephCapacityOSDStorageClass, t.CapacityStorageClass))
		}
		// Counts are Flux substitutions, so the value must read back as the
		// same decimal (YAML 1.1: 010 is octal 8).
		for _, k := range []string{KeyCephCapacityOSDCount, KeyCephCapacityOSDVolumeSizeGB} {
			if v := strings.TrimSpace(o.Sets[k]); !canonicalPositiveInt.MatchString(v) {
				errs = append(errs, fmt.Sprintf("%s must be a decimal whole number >= 1 without leading zeros when %s is set (got %q)", k, KeyCephCapacityOSDStorageClass, v))
			}
		}
		if t.OSDDeviceClass == "" {
			errs = append(errs, fmt.Sprintf("%s is required with a capacity device set, so the two sets get distinct device classes", KeyCephOSDDeviceClass))
		} else if t.OSDDeviceClass == t.CapacityDeviceClass {
			errs = append(errs, fmt.Sprintf("%s and %s must differ (got %q for both)", KeyCephOSDDeviceClass, KeyCephCapacityOSDDeviceClass, t.OSDDeviceClass))
		}
		if v := strings.TrimSpace(o.Sets[KeyCephCapacityOSDPortable]); v != "" && !isPrefillBoolSpelling(v) {
			errs = append(errs, fmt.Sprintf("%s must be true or false (got %q)", KeyCephCapacityOSDPortable, v))
		}
	}

	// Pools: the class must exist, and must hold enough OSDs for the pool.
	primary, _ := pvcPrimaryOSDs(o)
	replicas, err := pvcReplicas(o)
	if err != nil {
		errs = append(errs, err.Error())
		replicas = cephPVCFleetDefaultOSDCount
	}
	osdsIn := func(dc string) (int, bool) {
		switch {
		case dc == "":
			n := primary
			if t.CapacityStorageClass != "" {
				n += t.CapacityOSDCount
			}
			return n, true
		case dc == t.OSDDeviceClass:
			return primary, true
		case t.CapacityStorageClass != "" && dc == t.CapacityDeviceClass:
			return t.CapacityOSDCount, true
		}
		return 0, false
	}
	pool := func(k, dc string, need int, what string) {
		n, ok := osdsIn(dc)
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf("%s %q is not provided by any OSD set (set %s or %s to it)", k, dc, KeyCephOSDDeviceClass, KeyCephCapacityOSDDeviceClass))
		case n < need:
			errs = append(errs, fmt.Sprintf("%s: %s needs %d OSDs on distinct hosts in device class %q, but the class has %d", k, what, need, dcOrAll(dc), n))
		}
	}
	replicated := fmt.Sprintf("the %d-copy", replicas)
	pool(KeyCephBlockPoolDeviceClass, t.BlockPoolDeviceClass, replicas, replicated+" block pool")
	pool(KeyCephRGWMetadataDeviceClass, t.RGWMetadataDeviceClass, replicas, replicated+" object-store metadata pool")
	if v := strings.TrimSpace(o.Sets[KeyCephRGWDataErasureCoding]); v != "" && !t.erasureCoded() {
		errs = append(errs, fmt.Sprintf("%s %q must be K+M with K >= %d, M >= %d and K+M <= %d, for example 4+2", KeyCephRGWDataErasureCoding, v,
			cephErasureCodingMinDataChunks, cephErasureCodingMinCodingChunk, cephErasureCodingMaxChunkTotal))
	}
	if t.erasureCoded() {
		pool(KeyCephRGWDataErasureCoding, t.RGWDataDeviceClass, t.RGWDataECDataChunks+t.RGWDataECCodingChunks,
			fmt.Sprintf("the %d+%d erasure-coded data pool", t.RGWDataECDataChunks, t.RGWDataECCodingChunks))
	} else {
		pool(KeyCephRGWDataDeviceClass, t.RGWDataDeviceClass, replicas, replicated+" object-store data pool")
	}
	return errs
}

func dcOrAll(dc string) string {
	if dc == "" {
		return "(all OSDs)"
	}
	return dc
}

func isPrefillBoolSpelling(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on", "false", "0", "no", "off":
		return true
	}
	return false
}

// pvcTierPatches renders the overlay patches for the tier keys, without the
// leading "patches:" line (the overlay already has one). Count and size are
// Flux substitutions, so raising CEPH_CAPACITY_OSD_COUNT in cluster-config.env
// adds OSDs later; validation accepts only canonical decimals for them. The
// StorageClass, device classes, erasure coding and portability are literal:
// changing them on a live cluster is a migration, not a substitution, and a
// literal keeps their YAML type (a class named 123 stays a string, portable a
// boolean) whatever the operator wrote.
func pvcTierPatches(t CephTiers) string {
	if !t.active() {
		return ""
	}
	var b strings.Builder
	patch := func(kind, name string, ops []map[string]any) {
		if len(ops) == 0 {
			return
		}
		body, _ := json.MarshalIndent(ops, "      ", "  ")
		fmt.Fprintf(&b, "  - target:\n      group: ceph.rook.io\n      version: v1\n      kind: %s\n      name: %s\n      namespace: rook-ceph\n    patch: |\n      %s\n", kind, name, body)
	}
	op := func(o, path string, value any) map[string]any {
		m := map[string]any{"op": o, "path": path}
		if o != "remove" {
			m["value"] = value
		}
		return m
	}

	var cluster []map[string]any
	if t.OSDDeviceClass != "" {
		// Fail the build, not the cluster, if the Fleet template's set or its
		// data claim moves. The template's claim has no annotations, so the
		// whole map is added.
		claim := "/spec/storage/storageClassDeviceSets/0/volumeClaimTemplates/0/metadata"
		cluster = append(cluster,
			op("test", "/spec/storage/storageClassDeviceSets/0/name", cephPVCPrimaryDeviceSet),
			op("test", claim+"/name", cephPVCDataClaimTemplate),
			op("add", claim+"/annotations", map[string]string{cephCrushDeviceClassAnnotation: t.OSDDeviceClass}))
	}
	if t.CapacityStorageClass != "" {
		cluster = append(cluster, op("add", "/spec/storage/storageClassDeviceSets/-", capacityDeviceSet(t)))
	}
	patch("CephCluster", "rook-ceph", cluster)

	if t.BlockPoolDeviceClass != "" {
		patch("CephBlockPool", "rbd-pool", []map[string]any{op("add", "/spec/deviceClass", t.BlockPoolDeviceClass)})
	}
	var store []map[string]any
	if t.RGWMetadataDeviceClass != "" {
		store = append(store, op("add", "/spec/metadataPool/deviceClass", t.RGWMetadataDeviceClass))
	}
	if t.RGWDataDeviceClass != "" {
		store = append(store, op("add", "/spec/dataPool/deviceClass", t.RGWDataDeviceClass))
	}
	if t.erasureCoded() {
		store = append(store,
			op("remove", "/spec/dataPool/replicated", nil),
			op("add", "/spec/dataPool/failureDomain", "host"),
			op("add", "/spec/dataPool/erasureCoded", map[string]int{
				"dataChunks": t.RGWDataECDataChunks, "codingChunks": t.RGWDataECCodingChunks,
			}))
	}
	patch("CephObjectStore", "my-store", store)
	return b.String()
}

// capacityDeviceSet mirrors the template's osd-template set (anti-affinity,
// resources, Block-mode claim) for the second StorageClass.
func capacityDeviceSet(t CephTiers) map[string]any {
	antiAffinity := func(apps ...string) map[string]any {
		return map[string]any{"podAntiAffinity": map[string]any{
			"preferredDuringSchedulingIgnoredDuringExecution": []map[string]any{{
				"weight": 100,
				"podAffinityTerm": map[string]any{
					"labelSelector": map[string]any{"matchExpressions": []map[string]any{{
						"key": "app", "operator": "In", "values": apps,
					}}},
					"topologyKey": "kubernetes.io/hostname",
				},
			}},
		}}
	}
	return map[string]any{
		"name":             cephPVCCapacityDeviceSet,
		"count":            "${" + KeyCephCapacityOSDCount + "}",
		"portable":         t.CapacityPortable,
		"tuneDeviceClass":  true,
		"encrypted":        false,
		"placement":        antiAffinity("rook-ceph-osd"),
		"preparePlacement": antiAffinity("rook-ceph-osd", "rook-ceph-osd-prepare"),
		"resources": map[string]any{
			"requests": map[string]string{"cpu": "100m", "memory": "1Gi"},
			"limits":   map[string]string{"memory": "4Gi"},
		},
		"volumeClaimTemplates": []map[string]any{{
			"metadata": map[string]any{
				"name":        cephPVCDataClaimTemplate,
				"annotations": map[string]string{cephCrushDeviceClassAnnotation: t.CapacityDeviceClass},
			},
			"spec": map[string]any{
				// Literal, not "${...}": Kustomize drops the quotes around a
				// whole-value substitution, so a class named 123 would be an int.
				"storageClassName": t.CapacityStorageClass,
				"accessModes":      []string{"ReadWriteOnce"},
				"resources":        map[string]any{"requests": map[string]string{"storage": "${" + KeyCephCapacityOSDVolumeSizeGB + "}Gi"}},
				"volumeMode":       "Block",
			},
		}},
	}
}
