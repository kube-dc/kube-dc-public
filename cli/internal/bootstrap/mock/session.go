package mock

// Session is the mock-only counterpart of bootstrap.Session — same
// shape, scenario-backed implementations of every port. The wire
// layer (`cli/internal/bootstrap/wire.go`) wraps this into a real
// bootstrap.Session by promoting the embedded ports.
//
// We keep this struct distinct from `bootstrap.Session` (rather than
// importing the parent package and constructing it here) to avoid an
// import cycle: bootstrap imports ports + mock + adapters, but mock
// and adapters can't import bootstrap.
type Session struct {
	Scenario *Scenario

	Scripts   *ScriptRunner
	Flux      *FluxClient
	K8s       *K8sClient
	OpenBao   *OpenBaoClient
	Git       *GitClient
	SOPS      *SOPSClient
	Systemctl *SystemctlClient
	Netplan   *NetplanClient
	DNS       *DNSClient
	SSH       *SSHClient
}

// NewSession loads the named scenario and constructs scenario-backed
// implementations for every port. Returns an error pointing at the
// available scenarios if the name doesn't resolve.
//
// Optional sentinel callback for the ScriptRunner — pass nil to drop
// sentinel-bracketed payloads (matches the production "no callback,
// no capture" default).
func NewSession(name string) (*Session, error) {
	s, err := Load(name)
	if err != nil {
		return nil, err
	}
	return &Session{
		Scenario:  s,
		Scripts:   NewScriptRunner(s, nil),
		Flux:      NewFluxClient(s),
		K8s:       NewK8sClient(s),
		OpenBao:   NewOpenBaoClient(s),
		Git:       NewGitClient(s),
		SOPS:      NewSOPSClient(s),
		Systemctl: NewSystemctlClient(s),
		Netplan:   NewNetplanClient(s),
		DNS:       NewDNSClient(s),
		SSH:       NewSSHClient(s),
	}, nil
}
