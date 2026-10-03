package ports

import "testing"

func TestParseSSHHostTarget(t *testing.T) {
	tests := []struct {
		input string
		want  SSHHost
	}{
		{"server-1", SSHHost{Alias: "server-1"}},
		{"ubuntu@server-1", SSHHost{Alias: "server-1", User: "ubuntu"}},
		{"127.0.0.1:2226", SSHHost{Alias: "127.0.0.1", Port: 2226}},
		{"ubuntu@127.0.0.1:2226", SSHHost{Alias: "127.0.0.1", User: "ubuntu", Port: 2226}},
		{"ubuntu@[2001:db8::1]:2226", SSHHost{Alias: "2001:db8::1", User: "ubuntu", Port: 2226}},
		{"2001:db8::1", SSHHost{Alias: "2001:db8::1"}},
		{"root@fe80::1%eth0", SSHHost{Alias: "fe80::1%eth0", User: "root"}},
		{"root@[fe80::1%eth0]", SSHHost{Alias: "fe80::1%eth0", User: "root"}},
		{"root@[fe80::1%eth0]:2226", SSHHost{Alias: "fe80::1%eth0", User: "root", Port: 2226}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSSHHostTarget(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("ParseSSHHostTarget(%q) = %+v, %v; want %+v", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestParseSSHHostTargetRejectsInvalidPort(t *testing.T) {
	for _, input := range []string{"", "@server", "user@", "user@@server", "server:", "server:0", "server:65536", "server:ssh", "user@server:abc", "[2001:db8::1]:0", "server name", "server\x00name"} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseSSHHostTarget(input); err == nil {
				t.Fatalf("accepted invalid SSH target %q", input)
			}
		})
	}
}
