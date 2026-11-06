package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// sshArgs returns the arguments of the ssh command that connects to the
// daemon of an ssh:// host, like the docker CLI: the remote `docker system
// dial-stdio` relays the connection to the remote daemon's socket.
func sshArgs(host string) ([]string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("invalid docker host %q: %w", host, err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("invalid docker host %q: missing host name", host)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("invalid docker host %q: ssh:// hosts take no path", host)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid docker host %q: ssh:// hosts take no query", host)
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return nil, fmt.Errorf("invalid docker host %q: passwords are not supported, use a key", host)
	}
	args := []string{"-o", "ConnectTimeout=30", "-T"}
	if user := u.User.Username(); user != "" {
		args = append(args, "-l", user)
	}
	if port := u.Port(); port != "" {
		args = append(args, "-p", port)
	}
	if strings.HasPrefix(u.Hostname(), "-") {
		return nil, fmt.Errorf("invalid docker host %q", host)
	}
	return append(args, "--", u.Hostname(), "docker", "system", "dial-stdio"), nil
}

// sshDialer returns a dial function that starts the ssh command for every
// connection.
func sshDialer(args []string) func(ctx context.Context, _, _ string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		path, err := exec.LookPath("ssh")
		if err != nil {
			return nil, errors.New("ssh:// docker hosts need the ssh command, which was not found in PATH")
		}
		// The command must outlive the dial context, which only bounds
		// the connection setup.
		cmd := exec.Command(path, args...)
		c := &cmdConn{cmd: cmd, stderrDone: make(chan struct{})}
		if c.w, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
		if c.r, err = cmd.StdoutPipe(); err != nil {
			return nil, err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start ssh: %w", err)
		}
		go func() {
			_, _ = io.Copy(&c.stderr, stderr)
			close(c.stderrDone)
		}()
		if err := ctx.Err(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	}
}

// cmdConn is a connection over the standard input and output of a command.
type cmdConn struct {
	cmd    *exec.Cmd
	r      io.ReadCloser
	w      io.WriteCloser
	stderr lockedBuffer
	// stderrDone is closed once the standard error of the command is
	// closed (usually when it exits) and copied to stderr.
	stderrDone chan struct{}
	closeOnce  sync.Once
}

// stderrWait bounds how long a read that reached the end of the output
// waits for the error message of ssh.
const stderrWait = 2 * time.Second

func (c *cmdConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if errors.Is(err, io.EOF) {
		// ssh closes its standard output when it exits, possibly before
		// its error message has been copied from its standard error.
		select {
		case <-c.stderrDone:
		case <-time.After(stderrWait):
		}
		if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
			return n, fmt.Errorf("ssh: %s", msg)
		}
	}
	return n, err
}

func (c *cmdConn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c *cmdConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.w.Close()
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	})
	return nil
}

type cmdAddr struct{}

func (cmdAddr) Network() string { return "cmd" }
func (cmdAddr) String() string  { return "ssh" }

func (c *cmdConn) LocalAddr() net.Addr              { return cmdAddr{} }
func (c *cmdConn) RemoteAddr() net.Addr             { return cmdAddr{} }
func (c *cmdConn) SetDeadline(time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(time.Time) error { return nil }

// lockedBuffer is a bytes.Buffer safe for concurrent use, bounded to the
// first 4 KiB written.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 4096 - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
