package setup

import (
	"context"
	"fmt"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Recovery reads retain the same selected SSH endpoint pins. Uploads are not
// available to a read-only inspector; the concrete drivers use only bounded reads.
type reviewedReadSSH struct {
	base  ports.CappedSSHHostKeyClient
	hosts []Host
}

func (s *reviewedReadSSH) target(given ports.SSHHost) (ports.SSHHost, error) {
	for _, h := range s.hosts {
		want := sshHost(h.SSHAlias)
		plain := given
		plain.ExpectedHostKeySHA256 = ""
		if want == plain && (given.ExpectedHostKeySHA256 == "" || given.ExpectedHostKeySHA256 == h.HostKeySHA256) {
			want.ExpectedHostKeySHA256 = h.HostKeySHA256
			return want, nil
		}
	}
	return ports.SSHHost{}, fmt.Errorf("inspection SSH endpoint differs from reviewed hosts")
}
func (s *reviewedReadSSH) Run(ctx context.Context, host ports.SSHHost, cmd string) ([]byte, error) {
	host, err := s.target(host)
	if err != nil {
		return nil, err
	}
	body, key, err := s.base.RunCappedWithHostKey(ctx, host, cmd, 64<<10)
	if err != nil || key.FingerprintSHA256 != host.ExpectedHostKeySHA256 {
		return nil, fmt.Errorf("pinned inspection SSH read failed")
	}
	return body, nil
}
func (s *reviewedReadSSH) Fetch(ctx context.Context, host ports.SSHHost, path string) ([]byte, error) {
	host, err := s.target(host)
	if err != nil {
		return nil, err
	}
	return s.base.Fetch(ctx, host, path)
}
func (s *reviewedReadSSH) Put(context.Context, ports.SSHHost, string, []byte, uint32) error {
	return fmt.Errorf("recovery inspection cannot upload files")
}
