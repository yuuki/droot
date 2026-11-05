package docker

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMain lets the test binary act as a fake ssh command (see
// TestClientSSH): it checks its arguments and relays its standard input
// and output to the unix socket in $DROOT_FAKE_SSH_SOCKET.
func TestMain(m *testing.M) {
	if sock := os.Getenv("DROOT_FAKE_SSH_SOCKET"); sock != "" {
		want := []string{"-o", "ConnectTimeout=30", "-T", "-l", "me", "-p", "2222", "--", "remote", "docker", "system", "dial-stdio"}
		if !slices.Equal(os.Args[1:], want) {
			_, _ = io.WriteString(os.Stderr, "unexpected arguments: "+strings.Join(os.Args[1:], " "))
			os.Exit(255)
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			_, _ = io.WriteString(os.Stderr, err.Error())
			os.Exit(255)
		}
		go func() {
			_, _ = io.Copy(conn, os.Stdin)
			_ = conn.(*net.UnixConn).CloseWrite()
		}()
		_, _ = io.Copy(os.Stdout, conn)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSSHArgs(t *testing.T) {
	for host, want := range map[string]string{
		"ssh://host":                "-o ConnectTimeout=30 -T -- host docker system dial-stdio",
		"ssh://me@host:2222":        "-o ConnectTimeout=30 -T -l me -p 2222 -- host docker system dial-stdio",
		"ssh://me@[2001:db8::1]:22": "-o ConnectTimeout=30 -T -l me -p 22 -- 2001:db8::1 docker system dial-stdio",
	} {
		got, err := sshArgs(host)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("sshArgs(%q) = %q, %v; want %q", host, got, err, want)
		}
	}
	for _, host := range []string{"ssh://me:secret@host", "ssh://host/var/run/docker.sock", "ssh://host?x=1", "ssh://-oProxyCommand=x", "ssh://"} {
		if _, err := sshArgs(host); err == nil {
			t.Errorf("sshArgs(%q) should fail", host)
		}
	}
}

func TestClientSSH(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	d := &fakeDaemon{}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: d}}
	srv.Start()
	defer srv.Close()

	// A fake ssh in PATH: this test binary, see TestMain.
	bin := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("DROOT_FAKE_SSH_SOCKET", sock)

	c, err := NewClient(Endpoint{Host: "ssh://me@remote:2222"})
	if err != nil {
		t.Fatal(err)
	}
	exerciseClient(t, c, d)

	// Errors of ssh are reported.
	c, err = NewClient(Endpoint{Host: "ssh://other@remote:2222"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(t.Context()); err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Errorf("want the error of ssh, got %v", err)
	}
}
