package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
)

// Typed platform.kube-dc.com operations, used where the installation serves
// them (the backend audits them itself, so no audit gate); the backend route
// everywhere else. Results keep the backend route's shape.

func (s *secretsScope) servesPlatform(ctx context.Context, r k8sapi.Resource) (*k8sapi.Client, bool) {
	caps, err := s.capabilities()
	if err != nil || !caps.Serves(ctx, k8sapi.PlatformGroupVersion, r.Plural) {
		return nil, false
	}
	k8s, err := s.k8s()
	if err != nil {
		return nil, false
	}
	return k8s, true
}

func statusString(o *k8sapi.Object, key string) string {
	v, _ := o.Status[key].(string)
	return v
}

func statusInt(o *k8sapi.Object, key string) int {
	switch v := o.Status[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func (s *secretsScope) rotateKMSKey(ctx context.Context, name string) (*backend.RotateKMSKeyResult, error) {
	if k8s, ok := s.servesPlatform(ctx, k8sapi.KMSKeyRotations); ok {
		if _, err := k8s.Create(ctx, k8sapi.KMSKeyRotations, s.Namespace, "", map[string]any{"keyRef": map[string]any{"name": name}}); err != nil {
			return nil, err
		}
		return &backend.RotateKMSKeyResult{Name: name, Namespace: s.Namespace, Rotated: true}, nil
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.RotateKMSKey(ctx, s.Namespace, name)
}

func (s *secretsScope) getDBCredentials(ctx context.Context, name string) (*backend.DBCredentials, error) {
	if k8s, ok := s.servesPlatform(ctx, k8sapi.DatabaseCredentials); ok {
		o, err := k8s.Get(ctx, k8sapi.DatabaseCredentials, s.Namespace, name)
		if err != nil {
			return nil, err
		}
		return &backend.DBCredentials{
			Username: statusString(o, "username"), Password: statusString(o, "password"),
			LastVaultRotation: statusString(o, "lastVaultRotation"), RotationPeriod: statusInt(o, "rotationPeriod"), TTL: statusInt(o, "ttl"),
		}, nil
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.GetDBCredentials(ctx, s.Namespace, name)
}

func (s *secretsScope) issueDBCredentials(ctx context.Context, name string) (*backend.DBLease, error) {
	if k8s, ok := s.servesPlatform(ctx, k8sapi.DatabaseCredentialIssues); ok {
		o, err := k8s.Create(ctx, k8sapi.DatabaseCredentialIssues, s.Namespace, "", map[string]any{"policyRef": map[string]any{"name": name}})
		if err != nil {
			return nil, err
		}
		return &backend.DBLease{Username: statusString(o, "username"), Password: statusString(o, "password"), LeaseId: statusString(o, "leaseId"), LeaseDuration: statusInt(o, "leaseDuration")}, nil
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.IssueDBCredentials(ctx, s.Namespace, name)
}

func (s *secretsScope) rotateDBCredentialPolicy(ctx context.Context, name string, rotateRoot bool) (*backend.RotateDBCredentialPolicyResult, error) {
	// --root is retired; only the backend route answers it (HTTP 410).
	if k8s, ok := s.servesPlatform(ctx, k8sapi.DatabaseCredentialRotations); ok && !rotateRoot {
		if _, err := k8s.Create(ctx, k8sapi.DatabaseCredentialRotations, s.Namespace, "", map[string]any{"policyRef": map[string]any{"name": name}}); err != nil {
			return nil, err
		}
		return &backend.RotateDBCredentialPolicyResult{Rotated: true, Target: name, ReconcileTriggered: true}, nil
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.RotateDBCredentialPolicy(ctx, s.Namespace, name, rotateRoot)
}

func (s *secretsScope) destroySecretVersion(ctx context.Context, name string, version int) (*backend.DestroyVersionResult, error) {
	if version <= 0 {
		return nil, fmt.Errorf("destroy version: version must be > 0")
	}
	if k8s, ok := s.servesPlatform(ctx, k8sapi.SecretVersionDestructions); ok {
		if _, err := k8s.Create(ctx, k8sapi.SecretVersionDestructions, s.Namespace, "", map[string]any{"secretRef": map[string]any{"name": name}, "versions": []int{version}}); err != nil {
			return nil, err
		}
		return &backend.DestroyVersionResult{Name: name, Namespace: s.Namespace, DestroyedVersion: version}, nil
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.DestroyKVVersion(ctx, s.Namespace, name, version)
}

// getSecretWithValue is `secrets get --value`: the ManagedSecret from the
// kube-apiserver and its value from the typed operation, in the backend
// route's shape. Managed-service credentials keep the backend route.
func (s *secretsScope) getSecretWithValue(ctx context.Context, name string) (*backend.SecretSummary, error) {
	if k8s, ok := s.servesPlatform(ctx, k8sapi.ManagedSecretValues); ok {
		ms, err := k8s.GetManagedSecret(ctx, s.Namespace, name)
		if err != nil {
			return nil, err
		}
		if ms.Spec.PublicationRef == nil {
			v, err := k8s.GetSecretValue(ctx, s.Namespace, name)
			if err != nil {
				return nil, err
			}
			return secretSummary(ms, v), nil
		}
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.GetSecret(ctx, s.Namespace, name, true)
}

func secretSummary(ms *k8sapi.ManagedSecret, v *k8sapi.SecretValue) *backend.SecretSummary {
	out := &backend.SecretSummary{
		Name: ms.Metadata.Name, Namespace: ms.Metadata.Namespace, Type: ms.Spec.Type, Description: ms.Spec.Description,
		Sync: backend.SecretSync{Enabled: ms.Spec.Sync.Enabled, TargetSecretName: ms.Spec.Sync.TargetSecretName,
			RefreshInterval: ms.Spec.Sync.RefreshInterval, Keys: ms.Spec.Sync.Keys},
		Status:       backend.SecretStatus{Conditions: ms.Status.Conditions, SyncedSecretName: ms.Status.SyncedSecretName},
		ValueMissing: v.Status.ValueMissing,
	}
	if st := v.Status.Storage; st != nil {
		out.OpenBao = &backend.SecretOpenBao{CurrentVersion: st.CurrentVersion, CreatedTime: st.CreatedTime, UpdatedTime: st.UpdatedTime,
			MaxVersions: st.MaxVersions, CasRequired: st.CasRequired, CustomMetadata: st.CustomMetadata}
	}
	if !v.Status.ValueMissing {
		out.Value = &backend.SecretValue{Data: v.Data, Metadata: backend.SecretValueMeta{Version: v.Status.Version, CreatedTime: v.Status.CreatedTime}}
	}
	return out
}

// destroySecret is `secrets delete --destroy`: every stored version first
// (typed, allVersions), then the ManagedSecret, as the backend route orders
// it. An installation whose operation predates allVersions refuses the
// request before destroying anything; the backend route is used then.
func (s *secretsScope) destroySecret(ctx context.Context, name string) (*backend.DeleteSecretResult, error) {
	if k8s, ok := s.servesPlatform(ctx, k8sapi.SecretVersionDestructions); ok {
		ms, err := k8s.GetManagedSecret(ctx, s.Namespace, name)
		if err != nil {
			return nil, err
		}
		if ms.Spec.PublicationRef != nil {
			return nil, fmt.Errorf("managed-service credentials are read-only here; use service rotation or delete the ServiceBinding")
		}
		_, err = k8s.Create(ctx, k8sapi.SecretVersionDestructions, s.Namespace, "", map[string]any{"secretRef": map[string]any{"name": name}, "allVersions": true})
		var apiErr *k8sapi.APIError
		switch {
		case err == nil:
			if err := k8s.DeleteManagedSecret(ctx, s.Namespace, name); err != nil && !(errors.As(err, &apiErr) && apiErr.Status == 404) {
				return nil, fmt.Errorf("stored values destroyed but deleting the ManagedSecret failed: %w (run `kube-dc secrets delete %s`)", err, name)
			}
			return &backend.DeleteSecretResult{Name: name, Namespace: s.Namespace, Deleted: true, Destroyed: true}, nil
		case errors.As(err, &apiErr) && (apiErr.Status == 400 || apiErr.Status == 422):
			// An older operation without allVersions: the backend route.
		default:
			return nil, err
		}
	}
	cli, err := s.backend()
	if err != nil {
		return nil, err
	}
	return cli.DeleteSecret(ctx, s.Namespace, name, true)
}
