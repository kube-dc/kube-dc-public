package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/backend"
	"github.com/shalb/kube-dc/cli/internal/k8sapi"
)

// `secrets consumers`: the workloads in the Project whose pod spec references
// the synced Kubernetes Secret, read with the caller's own token. The same
// scan as the console's "Used by" panel (ui/backend/util/consumerScan.js):
// envFrom, env secretKeyRef, secret and projected volumes, imagePullSecrets;
// Pods, ReplicaSets and Jobs are rolled up into their controllers.

const consumerScannerVersion = "cli-1"

var consumerKinds = []struct{ kind, apiPath, resource string }{
	{"Pod", "/api/v1", "pods"},
	{"Deployment", "/apis/apps/v1", "deployments"},
	{"StatefulSet", "/apis/apps/v1", "statefulsets"},
	{"DaemonSet", "/apis/apps/v1", "daemonsets"},
	{"ReplicaSet", "/apis/apps/v1", "replicasets"},
	{"Job", "/apis/batch/v1", "jobs"},
	{"CronJob", "/apis/batch/v1", "cronjobs"},
}

type scanContainer struct {
	Name    string `json:"name"`
	EnvFrom []struct {
		SecretRef *struct {
			Name string `json:"name"`
		} `json:"secretRef"`
	} `json:"envFrom"`
	Env []struct {
		Name      string `json:"name"`
		ValueFrom *struct {
			SecretKeyRef *struct {
				Name string `json:"name"`
			} `json:"secretKeyRef"`
		} `json:"valueFrom"`
	} `json:"env"`
}

type scanPodSpec struct {
	Containers          []scanContainer `json:"containers"`
	InitContainers      []scanContainer `json:"initContainers"`
	EphemeralContainers []scanContainer `json:"ephemeralContainers"`
	Volumes             []struct {
		Name   string `json:"name"`
		Secret *struct {
			SecretName string `json:"secretName"`
		} `json:"secret"`
		Projected *struct {
			Sources []struct {
				Secret *struct {
					Name string `json:"name"`
				} `json:"secret"`
			} `json:"sources"`
		} `json:"projected"`
	} `json:"volumes"`
	ImagePullSecrets []struct {
		Name string `json:"name"`
	} `json:"imagePullSecrets"`
}

type scanTemplate struct {
	Spec *scanPodSpec `json:"spec"`
}

type scanWorkload struct {
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		OwnerReferences []struct {
			Kind       string `json:"kind"`
			Controller bool   `json:"controller"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		scanPodSpec
		Template    *scanTemplate `json:"template"`
		JobTemplate *struct {
			Spec struct {
				Template *scanTemplate `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
	} `json:"spec"`
}

func (w *scanWorkload) podSpec(kind string) *scanPodSpec {
	switch kind {
	case "Pod":
		return &w.Spec.scanPodSpec
	case "CronJob":
		if w.Spec.JobTemplate != nil && w.Spec.JobTemplate.Spec.Template != nil {
			return w.Spec.JobTemplate.Spec.Template.Spec
		}
	default:
		if w.Spec.Template != nil {
			return w.Spec.Template.Spec
		}
	}
	return nil
}

// rolledUp says the workload is reported through its controller instead.
func (w *scanWorkload) rolledUp(kind string) bool {
	for _, o := range w.Metadata.OwnerReferences {
		if !o.Controller {
			continue
		}
		switch {
		case kind == "Pod" && (o.Kind == "ReplicaSet" || o.Kind == "StatefulSet" || o.Kind == "DaemonSet" || o.Kind == "Job"),
			kind == "ReplicaSet" && o.Kind == "Deployment",
			kind == "Job" && o.Kind == "CronJob":
			return true
		}
		return false
	}
	return false
}

func secretRefsInPodSpec(spec *scanPodSpec, secret string) []string {
	if spec == nil {
		return nil
	}
	found := map[string]bool{}
	name := func(n string) string { return fmtCoalesce(n, "?") }
	for _, group := range [][]scanContainer{spec.Containers, spec.InitContainers, spec.EphemeralContainers} {
		for _, c := range group {
			for _, ef := range c.EnvFrom {
				if ef.SecretRef != nil && ef.SecretRef.Name == secret {
					found["envFrom:"+name(c.Name)] = true
				}
			}
			for _, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == secret {
					found["env:"+name(c.Name)+"/"+name(e.Name)] = true
				}
			}
		}
	}
	for _, v := range spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == secret {
			found["volume:"+name(v.Name)] = true
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.Secret != nil && src.Secret.Name == secret {
					found["projected:"+name(v.Name)] = true
				}
			}
		}
	}
	for _, ips := range spec.ImagePullSecrets {
		if ips.Name == secret {
			found["imagePullSecret"] = true
		}
	}
	refs := make([]string, 0, len(found))
	for r := range found {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	return refs
}

// listConsumers scans for the ManagedSecret's synced Secret (its
// spec.sync.targetSecretName, else its own name). A kind the caller cannot
// list lands in Errors; the rest is still reported.
func (s *secretsScope) listConsumers(ctx context.Context, name string) (*backend.ConsumerList, error) {
	k8s, err := s.k8s()
	if err != nil {
		return nil, err
	}
	target := name
	ms, err := k8s.GetManagedSecret(ctx, s.Namespace, name)
	var apiErr *k8sapi.APIError
	switch {
	case err == nil:
		if t := strings.TrimSpace(ms.Spec.Sync.TargetSecretName); t != "" {
			target = t
		}
	case errors.As(err, &apiErr) && apiErr.Status == 404:
	default:
		return nil, err
	}
	out := &backend.ConsumerList{Items: []backend.ConsumerItem{}, Secret: target, ScannerVersion: consumerScannerVersion}
	seen := map[string]bool{}
	for _, k := range consumerKinds {
		var list struct {
			Items []scanWorkload `json:"items"`
		}
		if err := k8s.ListRaw(ctx, k.apiPath, s.Namespace, k.resource, &list); err != nil {
			e := map[string]any{"kind": k.kind, "error": err.Error()}
			if errors.As(err, &apiErr) {
				e["status"] = apiErr.Status
			}
			out.Errors = append(out.Errors, e)
			continue
		}
		for i := range list.Items {
			w := &list.Items[i]
			if w.rolledUp(k.kind) {
				continue
			}
			refs := secretRefsInPodSpec(w.podSpec(k.kind), target)
			key := k.kind + "/" + w.Metadata.Name
			if len(refs) == 0 || seen[key] {
				continue
			}
			seen[key] = true
			out.Items = append(out.Items, backend.ConsumerItem{Kind: k.kind, Name: w.Metadata.Name, Namespace: w.Metadata.Namespace, References: refs})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Kind != out.Items[j].Kind {
			return out.Items[i].Kind < out.Items[j].Kind
		}
		return out.Items[i].Name < out.Items[j].Name
	})
	if len(out.Errors) == len(consumerKinds) {
		return nil, fmt.Errorf("cannot list workloads in %s: %v", s.Namespace, out.Errors[0]["error"])
	}
	return out, nil
}
