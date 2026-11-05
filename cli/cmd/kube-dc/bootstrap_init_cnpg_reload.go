package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// A platform database can start before Rook has created the Secret its backup
// configuration references (the bucket claim is applied in the same Flux
// layer). The CNPG instance manager then caches the miss and WAL archiving
// fails ("failed to get envs: cache miss") until something makes it reconcile
// again; found on a fresh ten-node installation, where Keycloak's database
// archived nothing for eight hours. CNPG's reload annotation is that signal:
// the instance managers re-read their configuration and credentials without
// restarting PostgreSQL.
const cnpgReloadAnnotation = "cnpg.io/reloadedAt"

type cnpgReloader interface {
	ListPlatformCNPGClusters(ctx context.Context) ([]map[string]any, error)
	AnnotateCNPGCluster(ctx context.Context, namespace, name, key, value string) error
	ListCNPGBackups(ctx context.Context, namespace string) ([]map[string]any, error)
	DeleteCNPGBackup(ctx context.Context, namespace, name string) error
	CreateCNPGBackup(ctx context.Context, backup map[string]any) error
}

var (
	cnpgReloadWait = 3 * time.Minute
	cnpgReloadPoll = 10 * time.Second
)

// reloadStalledPlatformArchiving asks CNPG to reload every Flux-managed
// (platform) cluster whose WAL archiving fails with the credential cache miss,
// waits briefly for archiving to work, and then clears the Backups such a
// cluster left in phase walArchivingFailing: CNPG never finishes them, and
// its ScheduledBackup skips every later run while one exists ("already taking
// a backup"). Such a Backup never copied data. Tenant databases are owned by
// the managed-services hub and are left alone.
func reloadStalledPlatformArchiving(ctx context.Context, out io.Writer, k cnpgReloader, now time.Time) error {
	clusters, err := k.ListPlatformCNPGClusters(ctx)
	if err != nil {
		return err
	}
	reloaded := map[string]bool{}
	for _, c := range clusters {
		ns, name := nestedString(c, "metadata", "namespace"), nestedString(c, "metadata", "name")
		if ns == "" || name == "" || !archivingCacheMiss(c) {
			continue
		}
		if err := k.AnnotateCNPGCluster(ctx, ns, name, cnpgReloadAnnotation, now.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("reload %s/%s: %w", ns, name, err)
		}
		reloaded[ns+"/"+name] = true
		fmt.Fprintf(out, "[finalize] %s/%s: WAL archiving started before its bucket credentials existed; asked CNPG to reload them\n", ns, name)
	}
	unblocked := map[string]bool{}
	deadline := time.Now().Add(cnpgReloadWait)
	for {
		pending := false
		for _, c := range clusters {
			ns, name := nestedString(c, "metadata", "namespace"), nestedString(c, "metadata", "name")
			key := ns + "/" + name
			switch {
			case unblocked[key]:
			case archivingWorks(c):
				if err := clearFailedArchivingBackups(ctx, out, k, ns, name); err != nil {
					fmt.Fprintf(out, "[finalize] %s: %v\n", key, err)
				}
				unblocked[key] = true
			case reloaded[key]:
				pending = true
			}
		}
		if !pending {
			return nil
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(out, "[finalize] WAL archiving did not recover within %s after the reload; check the clusters' ContinuousArchiving condition\n", cnpgReloadWait)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cnpgReloadPoll):
		}
		if clusters, err = k.ListPlatformCNPGClusters(ctx); err != nil {
			return err
		}
	}
}

func clearFailedArchivingBackups(ctx context.Context, out io.Writer, k cnpgReloader, ns, cluster string) error {
	backups, err := k.ListCNPGBackups(ctx, ns)
	if err != nil {
		return err
	}
	for _, b := range backups {
		name := nestedString(b, "metadata", "name")
		// A Backup that has started may be copying data; only one that
		// never started is safe to remove.
		if nestedString(b, "spec", "cluster", "name") != cluster || nestedString(b, "status", "phase") != "walArchivingFailing" ||
			nestedString(b, "status", "startedAt") != "" || name == "" {
			continue
		}
		// The stuck one may be the cluster's first (immediate) backup; without
		// a replacement the cluster has no base backup until its next daily
		// run. The replacement keeps the spec and owner (its ScheduledBackup)
		// and is created first, so a failure at either step leaves a state
		// the next run finishes.
		if err := k.CreateCNPGBackup(ctx, retryBackup(b, ns, name+"-retry")); err != nil {
			return fmt.Errorf("replace stuck backup %s/%s: %w", ns, name, err)
		}
		if err := k.DeleteCNPGBackup(ctx, ns, name); err != nil {
			return fmt.Errorf("delete stuck backup %s/%s: %w", ns, name, err)
		}
		fmt.Fprintf(out, "[finalize] %s/%s: replaced backup %s, stuck in walArchivingFailing, with %s-retry; the daily schedule runs again\n", ns, cluster, name, name)
	}
	return nil
}

// retryBackup is a new Backup with the stuck one's spec, labels and owner.
func retryBackup(stuck map[string]any, namespace, name string) map[string]any {
	meta, _ := stuck["metadata"].(map[string]any)
	newMeta := map[string]any{"name": name, "namespace": namespace}
	if labels, ok := meta["labels"]; ok {
		newMeta["labels"] = labels
	}
	if owners, ok := meta["ownerReferences"]; ok {
		newMeta["ownerReferences"] = owners
	}
	return map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Backup",
		"metadata":   newMeta,
		"spec":       stuck["spec"],
	}
}

func archivingWorks(cluster map[string]any) bool {
	status, _ := cluster["status"].(map[string]any)
	conds, _ := status["conditions"].([]any)
	for _, raw := range conds {
		c, _ := raw.(map[string]any)
		if c["type"] == "ContinuousArchiving" {
			return c["status"] == "True"
		}
	}
	return false
}

func archivingCacheMiss(cluster map[string]any) bool {
	status, _ := cluster["status"].(map[string]any)
	conds, _ := status["conditions"].([]any)
	for _, raw := range conds {
		c, _ := raw.(map[string]any)
		if c["type"] == "ContinuousArchiving" && c["status"] == "False" {
			msg, _ := c["message"].(string)
			return strings.Contains(msg, "cache miss")
		}
	}
	return false
}

func (k managedServicesKubectl) ListPlatformCNPGClusters(ctx context.Context) ([]map[string]any, error) {
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=15s", "get", "clusters.postgresql.cnpg.io",
		"--all-namespaces", "-l", "kustomize.toolkit.fluxcd.io/name,kustomize.toolkit.fluxcd.io/namespace=flux-system", "-o", "json")
	output, err := exec.CommandContext(callCtx, "kubectl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("list platform CNPG clusters: %w%s", err, kubectlStderr(err))
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(output, &list); err != nil {
		return nil, fmt.Errorf("decode platform CNPG clusters: %w", err)
	}
	return list.Items, nil
}

func (k managedServicesKubectl) AnnotateCNPGCluster(ctx context.Context, namespace, name, key, value string) error {
	if err := ports.BeforeMutation(ctx); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=15s", "-n", namespace, "annotate",
		"clusters.postgresql.cnpg.io", name, "--overwrite", "--field-manager=kube-dc-cli", key+"="+value)
	if output, err := exec.CommandContext(callCtx, "kubectl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (k managedServicesKubectl) ListCNPGBackups(ctx context.Context, namespace string) ([]map[string]any, error) {
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=15s", "-n", namespace, "get", "backups.postgresql.cnpg.io", "-o", "json")
	output, err := exec.CommandContext(callCtx, "kubectl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("list CNPG backups in %s: %w%s", namespace, err, kubectlStderr(err))
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(output, &list); err != nil {
		return nil, fmt.Errorf("decode CNPG backups in %s: %w", namespace, err)
	}
	return list.Items, nil
}

func (k managedServicesKubectl) DeleteCNPGBackup(ctx context.Context, namespace, name string) error {
	if err := ports.BeforeMutation(ctx); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=20s", "-n", namespace, "delete", "backups.postgresql.cnpg.io", name, "--ignore-not-found")
	if output, err := exec.CommandContext(callCtx, "kubectl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func kubectlStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return ": " + strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}

func (k managedServicesKubectl) CreateCNPGBackup(ctx context.Context, backup map[string]any) error {
	if err := ports.BeforeMutation(ctx); err != nil {
		return err
	}
	body, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append(k.kubectlArgs(), "--request-timeout=20s", "create", "--field-manager=kube-dc-cli", "-f", "-")
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	cmd.Stdin = strings.NewReader(string(body))
	if output, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "AlreadyExists") {
			return nil // a previous run already replaced it
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
