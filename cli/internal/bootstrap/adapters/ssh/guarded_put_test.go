package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"golang.org/x/crypto/ssh"
)

func TestGuardedPutUsesSamePinnedSessionAndPreservesStdin(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	payload := []byte("binary\x00upload\nwith 'quotes' and $(literal)\n")
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			done <- err
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		ch, reqs, err := (<-channels).Accept()
		if err != nil {
			done <- err
			return
		}
		request := <-reqs
		var command struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &command); err != nil {
			done <- err
			return
		}
		if request.Type != "exec" || !strings.HasPrefix(command.Command, "ownership-guard ") || !strings.Contains(command.Command, "mktemp") || strings.Contains(command.Command, string(payload)) {
			done <- fmt.Errorf("incorrect guarded command: %s", command.Command)
			return
		}
		_ = request.Reply(true, nil)
		received, err := io.ReadAll(ch)
		if err == nil && !bytes.Equal(received, payload) {
			err = fmt.Errorf("upload stdin changed: %q", received)
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
		done <- err
		_ = server.Wait()
	}()
	client := New()
	client.loadAuth = func(string) ([]ssh.AuthMethod, error) { return nil, nil }
	client.hostKeyCallbackForTest = ssh.InsecureIgnoreHostKey()
	host := ports.SSHHost{Hostname: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, ExpectedHostKeySHA256: ssh.FingerprintSHA256(signer.PublicKey())}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	guards := 0
	err = client.PutGuarded(ctx, host, "/private/upload", payload, 0600, func(command string) (string, error) { guards++; return "ownership-guard " + command, nil })
	if err != nil {
		t.Fatal(err)
	}
	if guards != 1 {
		t.Fatal("guard did not wrap actual upload")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestGuardedPutRequiresGuard(t *testing.T) {
	if err := New().PutGuarded(context.Background(), ports.SSHHost{}, "/private/upload", []byte("data"), 0600, nil); err == nil {
		t.Fatal("unguarded upload accepted")
	}
}
