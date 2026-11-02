package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type fakeCNPGReloader struct {
	clusters  []map[string]any
	backups   map[string][]map[string]any
	annotated []string
	deleted   []string
	// afterReload replaces the cluster list once a reload was requested.
	afterReload []map[string]any
}

func (f *fakeCNPGReloader) ListPlatformCNPGClusters(context.Context) ([]map[string]any, error) {
	if len(f.annotated) > 0 && f.afterReload != nil {
		return f.afterReload, nil
	}
	return f.clusters, nil
}

func (f *fakeCNPGReloader) AnnotateCNPGCluster(_ context.Context, ns, name, key, value string) error {
	f.annotated = append(f.annotated, ns+"/"+name+" "+key+"="+value)
	return nil
}

func (f *fakeCNPGReloader) ListCNPGBackups(_ context.Context, ns string) ([]map[string]any, error) {
	return f.backups[ns], nil
}

func (f *fakeCNPGReloader) DeleteCNPGBackup(_ context.Context, ns, name string) error {
	f.deleted = append(f.deleted, ns+"/"+name)
	return nil
}

func cnpgBackup(cluster, name, phase string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"cluster": map[string]any{"name": cluster}},
		"status":   map[string]any{"phase": phase},
	}
}

func startedBackup(b map[string]any) map[string]any {
	b["status"].(map[string]any)["startedAt"] = "2026-11-02T09:00:00Z"
	return b
}

func cnpgCluster(ns, name, status, message string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"namespace": ns, "name": name},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "ContinuousArchiving", "status": status, "message": message},
		}},
	}
}

// A platform database whose archiving failed on the credential cache miss is
// reloaded; once archiving works, the Backups it left in walArchivingFailing
// are removed so the schedule runs again. Healthy clusters only get their
// stuck Backups cleared; other archiving failures are left alone.
func TestReloadStalledPlatformArchiving(t *testing.T) {
	oldWait, oldPoll := cnpgReloadWait, cnpgReloadPoll
	cnpgReloadWait, cnpgReloadPoll = time.Second, time.Millisecond
	t.Cleanup(func() { cnpgReloadWait, cnpgReloadPoll = oldWait, oldPoll })
	f := &fakeCNPGReloader{
		clusters: []map[string]any{
			cnpgCluster("keycloak", "keycloak-db", "False", "failed to get envs: cache miss"),
			cnpgCluster("monitoring", "grafana-pg", "True", ""),
			cnpgCluster("other", "db", "False", "unexpected failure while invoking the archiver: 403"),
		},
		afterReload: []map[string]any{
			cnpgCluster("keycloak", "keycloak-db", "True", "Continuous archiving is working"),
			cnpgCluster("monitoring", "grafana-pg", "True", ""),
			cnpgCluster("other", "db", "False", "unexpected failure while invoking the archiver: 403"),
		},
		backups: map[string][]map[string]any{
			"keycloak": {
				cnpgBackup("keycloak-db", "keycloak-db-daily-1", "walArchivingFailing"),
				cnpgBackup("keycloak-db", "keycloak-db-daily-0", "completed"),
			},
			"monitoring": {
				cnpgBackup("grafana-pg", "grafana-pg-daily-1", "walArchivingFailing"),
				startedBackup(cnpgBackup("grafana-pg", "grafana-pg-daily-2", "walArchivingFailing")),
			},
			"other": {cnpgBackup("db", "db-1", "walArchivingFailing")},
		},
	}
	var out bytes.Buffer
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	if err := reloadStalledPlatformArchiving(context.Background(), &out, f, now); err != nil {
		t.Fatal(err)
	}
	if len(f.annotated) != 1 || f.annotated[0] != "keycloak/keycloak-db cnpg.io/reloadedAt=2026-11-02T09:00:00Z" {
		t.Fatalf("annotated %v", f.annotated)
	}
	got := strings.Join(f.deleted, ",")
	if got != "monitoring/grafana-pg-daily-1,keycloak/keycloak-db-daily-1" && got != "keycloak/keycloak-db-daily-1,monitoring/grafana-pg-daily-1" {
		t.Fatalf("deleted %v", f.deleted)
	}
	if !strings.Contains(out.String(), "keycloak/keycloak-db") {
		t.Fatalf("output %q", out.String())
	}
}

// A reload that does not fix archiving gives up after the wait and keeps the
// failed Backup for inspection.
func TestReloadStalledPlatformArchivingGivesUp(t *testing.T) {
	oldWait, oldPoll := cnpgReloadWait, cnpgReloadPoll
	cnpgReloadWait, cnpgReloadPoll = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { cnpgReloadWait, cnpgReloadPoll = oldWait, oldPoll })
	f := &fakeCNPGReloader{
		clusters: []map[string]any{cnpgCluster("keycloak", "keycloak-db", "False", "failed to get envs: cache miss")},
		backups:  map[string][]map[string]any{"keycloak": {cnpgBackup("keycloak-db", "b", "walArchivingFailing")}},
	}
	var out bytes.Buffer
	if err := reloadStalledPlatformArchiving(context.Background(), &out, f, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 || !strings.Contains(out.String(), "did not recover") {
		t.Fatalf("deleted %v output %q", f.deleted, out.String())
	}
}
