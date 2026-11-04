package clusterinit

import "testing"

// GRAFANA_TENANT_MODE feeds the manager's --grafana-tenant-mode; a typo must
// fail at install time, not reach the manager.
func TestValidateKnownSettingGrafanaTenantMode(t *testing.T) {
	for _, ok := range []string{"", "org", "project"} {
		if err := validateKnownSetting("GRAFANA_TENANT_MODE", ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	if info, ok := KnownSetting("GRAFANA_TENANT_MODE"); !ok || info.Format != "org or project" {
		t.Errorf("GRAFANA_TENANT_MODE must be a described setting, got %+v %v", info, ok)
	}
	for _, bad := range []string{"projects", "Project", "tenant"} {
		if err := validateKnownSetting("GRAFANA_TENANT_MODE", bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}
