package initform

import (
	"fmt"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// draftOptions maps the whole form, including incomplete drafts. Consent is
// required only for applying; all parsing errors still prevent a partial save.
func (s *State) draftOptions() (*clusterinit.InitOptions, error) {
	copy := *s
	copy.DisabledConsent = true
	o := &clusterinit.InitOptions{}
	if err := copy.Apply(o); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *State) configMap() (map[string]string, error) {
	o, err := s.draftOptions()
	if err != nil {
		return nil, err
	}
	return clusterinit.ExportMap(o), nil
}

// editedConfig uses the same importer as --config. Construct a fresh State so
// clearing a value cannot restore the prior value through an overlay merge.
func (s *State) editedConfig(key, value string, remove bool) (State, error) {
	if key == clusterinit.KeySpecVersion {
		return State{}, fmt.Errorf("the file format version cannot be edited")
	}
	values, err := s.configMap()
	if err != nil {
		return State{}, err
	}
	if remove {
		delete(values, key)
	} else {
		values[key] = value
	}
	if err := clusterinit.ValidateInputSpec(values); err != nil {
		return State{}, err
	}
	o := &clusterinit.InitOptions{}
	if ignored := clusterinit.ImportMap(o, values, func(string) bool { return false }); len(ignored) > 0 {
		return State{}, fmt.Errorf("unsupported input key %s", key)
	}
	next := State{ManagementAddress: s.ManagementAddress, DisabledConsent: s.DisabledConsent}
	// HostID is the primary-node input. Preserve the inspected host identity
	// while editing another key, but let a primary-node clear/remove take
	// effect before FromOptions overlays non-empty values.
	if key != clusterinit.KeyPrimaryNode {
		next.HostID = s.HostID
	}
	next.FromOptions(o)
	return next, nil
}

func (m *PanelModel) configurationFields() []panelField {
	fields := []panelField{
		{Section: "Configuration", Label: "Save path", Kind: panelText, Desc: "S saves the install input file here. Install with bootstrap init --config FILE.",
			Get: func(*State) string { return m.draftPath }, Set: func(_ *State, v string) { m.draftPath = v },
			Validate: func(v string) error {
				if strings.ContainsAny(v, "\r\n\x00") {
					return fmt.Errorf("enter a file path on one line")
				}
				return nil
			}},
		{Section: "Configuration", Label: "Find setting", Kind: panelText, Desc: "Filter environment key names, for example MTU, DNS, or SMTP.",
			Get: func(*State) string { return m.configFilter }, Set: func(_ *State, v string) { m.configFilter = strings.ToUpper(v) }},
	}
	if m.workflow != nil && m.workflow.services.Demo != nil {
		fields[0].Label = "Demo draft path"
		fields[0].Desc = "S saves the isolated JSON demo draft here. It cannot install a cluster."
		fields[0].Get = func(*State) string { return m.workflow.services.DraftPath }
		fields[0].Set = func(_ *State, v string) { m.workflow.services.DraftPath = v }
	}
	apply := func(key, value string, remove bool) error {
		next, err := m.st.editedConfig(key, value, remove)
		if err == nil {
			*m.st = next
		}
		return err
	}
	entry := func(raw string) (string, string, error) {
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return "", "", fmt.Errorf("enter KEY=VALUE; an empty value is allowed")
		}
		key = strings.TrimSpace(key)
		_, err := m.st.editedConfig(key, value, false)
		return key, value, err
	}
	fields = append(fields, panelField{Section: "Configuration", Label: "Set KEY=VALUE", Kind: panelText,
		Desc: "Add or replace an input. Keys use the Fleet environment names. Credential contents are not allowed.",
		Get:  func(*State) string { return "" }, Validate: func(v string) error { _, _, err := entry(v); return err },
		Set: func(_ *State, v string) {
			if v != "" {
				k, val, err := entry(v)
				if err == nil {
					_ = apply(k, val, false)
				}
			}
		}},
		panelField{Section: "Configuration", Label: "Remove override", Kind: panelText,
			Desc: "Enter a key to remove it. Clear a setting's value instead to keep an explicit empty override.",
			Get:  func(*State) string { return "" }, Validate: func(v string) error { _, err := m.st.editedConfig(v, "", true); return err },
			Set: func(_ *State, v string) {
				if v != "" {
					_ = apply(v, "", true)
				}
			}})
	values, err := m.st.configMap()
	if err != nil {
		return fields
	}
	for _, key := range clusterinit.SpecOrderedKeys(values) {
		if key == clusterinit.KeySpecVersion || !strings.Contains(key, m.configFilter) {
			continue
		}
		desc := "Fleet environment input. Check its selected manifest consumer. Empty keeps an explicit override."
		if info, known := clusterinit.KnownSetting(key); known {
			desc = info.Description + " Expected: " + info.Format + ". Empty keeps an explicit override."
		}
		fields = append(fields, panelField{Section: "Configuration", Label: key, Kind: panelText,
			Desc:     desc,
			Get:      func(*State) string { return values[key] },
			Validate: func(v string) error { _, err := m.st.editedConfig(key, v, false); return err },
			Set:      func(_ *State, v string) { _ = apply(key, v, false) }})
	}
	return fields
}
