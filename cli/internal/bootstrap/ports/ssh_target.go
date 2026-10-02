package ports

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

// ParseSSHHostTarget parses an SSH config alias or [user@]host[:port].
// A user or port overrides the corresponding ssh_config setting, while the
// host token still resolves through an SSH config Host block when present.
func ParseSSHHostTarget(raw string) (SSHHost, error) {
	if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return SSHHost{}, fmt.Errorf("SSH target must be an alias or [user@]host[:port]")
	}
	user, target, qualified := strings.Cut(raw, "@")
	if !qualified {
		user, target = "", raw
	} else if user == "" || target == "" || strings.Contains(target, "@") {
		return SSHHost{}, fmt.Errorf("SSH target must be an alias or [user@]host[:port]")
	}
	port := 0
	if strings.HasPrefix(target, "[") || strings.Count(target, ":") == 1 {
		if strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]") {
			target = strings.TrimSuffix(strings.TrimPrefix(target, "["), "]")
			if _, err := netip.ParseAddr(target); err != nil {
				return SSHHost{}, fmt.Errorf("invalid SSH target address")
			}
		} else {
			host, service, err := net.SplitHostPort(target)
			if err != nil || host == "" {
				return SSHHost{}, fmt.Errorf("SSH target port must be between 1 and 65535")
			}
			port, err = strconv.Atoi(service)
			if err != nil || port < 1 || port > 65535 {
				return SSHHost{}, fmt.Errorf("SSH target port must be between 1 and 65535")
			}
			target = host
		}
	} else if strings.Contains(target, ":") {
		if _, err := netip.ParseAddr(target); err != nil {
			return SSHHost{}, fmt.Errorf("invalid SSH target address")
		}
	}
	if target == "" || strings.ContainsAny(target, "/,=") {
		return SSHHost{}, fmt.Errorf("invalid SSH target address")
	}
	return SSHHost{Alias: target, User: user, Port: port}, nil
}
