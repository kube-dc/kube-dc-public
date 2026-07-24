package ports

import (
	"context"
	"time"
)

// GPUNodeRuntime is the minimum target-cluster state needed to prove that an
// SSH-discovered GPU host is the same Kubernetes Node and that mutually
// exclusive device-plugin owners do not coexist. Raw device identities and
// unrelated Pods are deliberately absent.
type GPUNodeRuntime struct {
	Name         string
	SystemUUID   string
	Labels       map[string]string
	PluginOwners []GPUPluginOwner
}

type GPUPluginOwner struct {
	Namespace string
	Kind      string
	Name      string
}

// GPUHolder is a live Pod or controller-owned workload that currently holds a
// native accelerator resource on one exact node. Resource names and Pod names
// stay admin-only; callers must not expose this state through tenant APIs.
type GPUHolder struct {
	Namespace string
	Kind      string
	Name      string
	Resources []string
}

// GPUDRADriverPod is the exact operator-owned kubelet-plugin state needed to
// diagnose a DaemonSet that is stuck behind a stale post-reboot Pod. It remains
// operator-only and contains no physical GPU identity.
type GPUDRADriverPod struct {
	Namespace               string    `json:"namespace"`
	Name                    string    `json:"name"`
	Node                    string    `json:"node"`
	Phase                   string    `json:"phase"`
	Ready                   bool      `json:"ready"`
	NodeReady               bool      `json:"nodeReady"`
	Deleting                bool      `json:"deleting"`
	CreationTimestamp       time.Time `json:"creationTimestamp"`
	ReadyLastTransitionTime time.Time `json:"readyLastTransitionTime"`
}

// GPUNodeTransitionState is the minimum state required by the holder-safe
// pod-hami <-> vm-passthrough transition engine.
type GPUNodeTransitionState struct {
	Runtime       GPUNodeRuntime
	Holders       []GPUHolder
	Ready         bool
	Unschedulable bool
}

// GPUClusterReader performs exact-name Node GETs and node-scoped Pod LISTs
// against an operator-supplied target kubeconfig.
type GPUClusterReader interface {
	GPUNodeRuntimes(ctx context.Context, nodes []string) (map[string]GPUNodeRuntime, error)
}

// GPUTransitionClient adds the two narrowly scoped mutations needed by the
// transition state machine. Reads use an exact Node GET plus a node-scoped Pod
// LIST; scheduling changes patch only spec.unschedulable on that Node.
type GPUTransitionClient interface {
	GPUNodeTransitionState(ctx context.Context, node string) (GPUNodeTransitionState, error)
	SetGPUNodeSchedulable(ctx context.Context, node string, schedulable bool) error
}

// GPUDRAStatus is the operator-only, redacted-enough live state used by
// bootstrap GPU doctor/plan. It contains workload names because migration and
// rollback must identify holders, but never leaves the operator CLI.
type GPUDRAStatus struct {
	ServerVersion            string
	StableAPI                bool
	ConsumableCapacity       bool
	Driver                   string
	DeviceClass              string
	DeviceClassPresent       bool
	ResourceSlices           int
	Devices                  int
	ShareableDevices         int
	Claims                   int
	GPUClaims                int
	AllocatedClaims          int
	PendingClaims            int
	DriverReady              int32
	DriverDesired            int32
	DriverPods               []GPUDRADriverPod
	DriverImages             []string
	AllocatorOwners          []string
	DRANodes                 []string
	WrongModeNodes           []string
	LegacyHolders            []GPUHolder
	DRAHolders               []GPUHolder
	SharedCreationEnabled    bool
	VMCreationEnabled        bool
	CanaryPresent            bool
	CanarySuspended          bool
	CanaryLastSuccessfulTime *time.Time
}

type GPUDRAReader interface {
	GPUDRAStatus(ctx context.Context, driver, deviceClass string) (GPUDRAStatus, error)
}
