package main

import (
	"context"
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
)

// kmsKeys is KMSKey CRUD, served by the Kubernetes API where the cluster has
// the KMSKey resource and by the backend's /api/kms routes on older
// installations. Both answer the backend's summary shape, so output does not
// depend on which one ran.
type kmsKeys interface {
	List(ctx context.Context, namespace string) (*backend.KMSKeyList, error)
	Get(ctx context.Context, namespace, name string) (*backend.KMSKeySummary, error)
	Create(ctx context.Context, namespace, name string, opts backend.CreateKMSKeyOptions) (*backend.KMSKeySummary, error)
	Delete(ctx context.Context, namespace, name string) (*backend.DeleteKMSKeyResult, error)
}

// kmsKeys picks the newest API this scope's cluster serves and audits.
func (s *secretsScope) kmsKeys(ctx context.Context) (kmsKeys, error) {
	if s.native(ctx, k8sapi.SecurityGroupVersion, k8sapi.KMSKeys.Plural) {
		k8s, err := s.k8s()
		if err != nil {
			return nil, err
		}
		return nativeKMSKeys{k8s}, nil
	}
	bc, err := s.backend()
	if err != nil {
		return nil, err
	}
	return legacyKMSKeys{bc}, nil
}

type legacyKMSKeys struct{ c *backend.Client }

func (l legacyKMSKeys) List(ctx context.Context, ns string) (*backend.KMSKeyList, error) {
	return l.c.ListKMSKeys(ctx, ns)
}
func (l legacyKMSKeys) Get(ctx context.Context, ns, name string) (*backend.KMSKeySummary, error) {
	return l.c.GetKMSKey(ctx, ns, name)
}
func (l legacyKMSKeys) Create(ctx context.Context, ns, name string, o backend.CreateKMSKeyOptions) (*backend.KMSKeySummary, error) {
	return l.c.CreateKMSKey(ctx, ns, name, o)
}
func (l legacyKMSKeys) Delete(ctx context.Context, ns, name string) (*backend.DeleteKMSKeyResult, error) {
	return l.c.DeleteKMSKey(ctx, ns, name)
}

type nativeKMSKeys struct{ c *k8sapi.Client }

func (n nativeKMSKeys) List(ctx context.Context, ns string) (*backend.KMSKeyList, error) {
	list, err := n.c.List(ctx, k8sapi.KMSKeys, ns)
	if err != nil {
		return nil, err
	}
	out := &backend.KMSKeyList{Items: make([]backend.KMSKeySummary, 0, len(list.Items))}
	for i := range list.Items {
		out.Items = append(out.Items, summariseKMSKey(&list.Items[i]))
	}
	return out, nil
}

func (n nativeKMSKeys) Get(ctx context.Context, ns, name string) (*backend.KMSKeySummary, error) {
	k, err := n.c.Get(ctx, k8sapi.KMSKeys, ns, name)
	if err != nil {
		return nil, err
	}
	s := summariseKMSKey(k)
	return &s, nil
}

func (n nativeKMSKeys) Create(ctx context.Context, ns, name string, o backend.CreateKMSKeyOptions) (*backend.KMSKeySummary, error) {
	spec, err := kmsKeySpec(o)
	if err != nil {
		return nil, err
	}
	k, err := n.c.Create(ctx, k8sapi.KMSKeys, ns, name, spec)
	if err != nil {
		return nil, err
	}
	s := summariseKMSKey(k)
	return &s, nil
}

func (n nativeKMSKeys) Delete(ctx context.Context, ns, name string) (*backend.DeleteKMSKeyResult, error) {
	if err := n.c.Delete(ctx, k8sapi.KMSKeys, ns, name); err != nil {
		return nil, err
	}
	return &backend.DeleteKMSKeyResult{Name: name, Namespace: ns, Deleted: true}, nil
}

// kmsKeySpec applies the defaults and the two checks the backend makes on
// top of the CRD's own validation (ui/backend/controllers/kmsModule.js).
func kmsKeySpec(o backend.CreateKMSKeyOptions) (map[string]any, error) {
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	if o.Rotation.Enabled && o.Rotation.Interval == "" {
		return nil, fmt.Errorf("rotation.interval is required when rotation.enabled=true")
	}
	rotation := map[string]any{"enabled": o.Rotation.Enabled}
	if o.Rotation.Interval != "" {
		rotation["interval"] = o.Rotation.Interval
	}
	return map[string]any{
		"purpose":        def(o.Purpose, "application"),
		"algorithm":      def(o.Algorithm, "aes256-gcm96"),
		"deletionPolicy": def(o.DeletionPolicy, "retain"),
		"rotation":       rotation,
		// Exportable keys are not offered (phase 1), as on the backend route.
		"exportable": false,
	}, nil
}

// summariseKMSKey is the backend's summariseKMSKey, so both paths print alike.
func summariseKMSKey(k *k8sapi.Object) backend.KMSKeySummary {
	str := func(m map[string]any, key, d string) string {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
		return d
	}
	num := func(m map[string]any, key string) int {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
		return 0
	}
	spec, status := k.Spec, k.Status
	if spec == nil {
		spec = map[string]any{}
	}
	if status == nil {
		status = map[string]any{}
	}
	s := backend.KMSKeySummary{
		Name: k.Metadata.Name, Namespace: k.Metadata.Namespace, CreationTimestamp: k.Metadata.CreationTimestamp,
		Purpose:        str(spec, "purpose", "application"),
		Algorithm:      str(spec, "algorithm", "aes256-gcm96"),
		DeletionPolicy: str(spec, "deletionPolicy", "retain"),
		Exportable:     spec["exportable"] == true,
		Status: backend.KMSKeyStatus{
			KeyId: str(status, "keyId", ""), CurrentVersion: num(status, "currentVersion"),
			MinDecryptionVersion: num(status, "minDecryptionVersion"), LastRotatedTime: str(status, "lastRotatedTime", ""),
			NextRotationTime: str(status, "nextRotationTime", ""), ScheduledDeletion: str(status, "scheduledDeletion", ""),
			Conditions: []map[string]any{},
		},
	}
	if r, ok := spec["rotation"].(map[string]any); ok {
		s.Rotation.Enabled = r["enabled"] == true
		s.Rotation.Interval = str(r, "interval", "")
	}
	if conds, ok := status["conditions"].([]any); ok {
		for _, c := range conds {
			if m, ok := c.(map[string]any); ok {
				s.Status.Conditions = append(s.Status.Conditions, m)
			}
		}
	}
	return s
}
