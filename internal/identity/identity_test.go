package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

func openRoot(t *testing.T, dir string) *osutil.Root {
	t.Helper()
	r, err := osutil.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := `# comment
root:x:0:0:root:/root:/bin/sh
app:x:1500:1600:App:/home/app:/bin/sh
worker:x:1501:1600::/srv/worker:/bin/sh
+nisuser
broken:x:notanumber:0::/:/bin/sh
`
	group := `root:x:0:
appgroup:x:1600:
docker:x:1700:app,worker
audio:x:1800: app
`
	if err := os.WriteFile(filepath.Join(root, "etc", "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "group"), []byte(group), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolve(t *testing.T) {
	root := openRoot(t, newRoot(t))
	uid, gid := os.Getuid(), os.Getgid()
	// The current identity is looked up in the container for HOME.
	var curName, curHome string
	if uid == 0 {
		curName, curHome = "root", "/root"
	}

	tests := []struct {
		name        string
		user, group string
		want        Credential
		wantErr     bool
	}{
		{
			name: "no user and no group keeps the current identity",
			want: Credential{UID: uid, GID: gid, Username: curName, Home: curHome},
		},
		{
			name: "user name uses the primary group and supplementary groups",
			user: "app",
			want: Credential{UID: 1500, GID: 1600, Groups: []int{1600, 1700, 1800}, Username: "app", Home: "/home/app"},
		},
		{
			name: "numeric uid known in the container",
			user: "1501",
			want: Credential{UID: 1501, GID: 1600, Groups: []int{1600, 1700}, Username: "worker", Home: "/srv/worker"},
		},
		{
			name:  "explicit group overrides the primary group",
			user:  "app",
			group: "docker",
			want:  Credential{UID: 1500, GID: 1700, Groups: []int{1700, 1800}, Username: "app", Home: "/home/app"},
		},
		{
			name:  "numeric group",
			user:  "app",
			group: "4242",
			want:  Credential{UID: 1500, GID: 4242, Groups: []int{4242, 1700, 1800}, Username: "app", Home: "/home/app"},
		},
		{
			name: "unknown numeric uid is accepted",
			user: "4000000",
			want: Credential{UID: 4000000, GID: gid, Groups: []int{gid}},
		},
		{
			name:  "group only",
			group: "appgroup",
			want:  Credential{UID: uid, GID: 1600, Groups: []int{1600}},
		},
		{
			name:    "unknown user name",
			user:    "no-such-user-droot",
			wantErr: true,
		},
		{
			name:    "unknown group name",
			group:   "no-such-group-droot",
			wantErr: true,
		},
		{
			name:    "negative uid is not numeric",
			user:    "-1",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(root, tt.user, tt.group)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.UID != tt.want.UID || got.GID != tt.want.GID || !slices.Equal(got.Groups, tt.want.Groups) ||
				got.Username != tt.want.Username || got.Home != tt.want.Home {
				t.Fatalf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestResolveWithoutDatabases(t *testing.T) {
	root := openRoot(t, t.TempDir()) // no /etc at all
	got, err := Resolve(root, "1234", "5678")
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != 1234 || got.GID != 5678 || !slices.Equal(got.Groups, []int{5678}) {
		t.Fatalf("unexpected credential %+v", *got)
	}
}

func TestResolveFallsBackToHost(t *testing.T) {
	// "root" exists on virtually every host; the container has no passwd.
	root := openRoot(t, t.TempDir())
	got, err := Resolve(root, "root", "")
	if err != nil {
		t.Skipf("host has no root user: %v", err)
	}
	if got.UID != 0 || got.Username != "root" {
		t.Fatalf("unexpected credential %+v", *got)
	}
}

func TestResolveReadsInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "root")
	hostEtc := filepath.Join(base, "hostetc")
	for _, d := range []string{rootDir, hostEtc} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// /etc of the image points to a host directory: it must be resolved
	// inside the root, where it does not exist.
	if err := os.WriteFile(filepath.Join(hostEtc, "passwd"), []byte("hostonly:x:4321:4321::/:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hostEtc, filepath.Join(rootDir, "etc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(openRoot(t, rootDir), "hostonly", ""); err == nil {
		t.Fatal("a symlink in the image must not make droot read the host's files")
	}
}

func TestResolveRefusesFIFO(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(rootDir, "etc", "passwd"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Resolve(openRoot(t, rootDir), "app", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO as /etc/passwd must be an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("droot hung on a FIFO /etc/passwd")
	}
}

func TestResolveWarnsAboutTheDefaultGroup(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	root := openRoot(t, t.TempDir())
	if _, err := Resolve(root, "4000001", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "use --group") {
		t.Fatalf("want a warning, got %q", buf.String())
	}
	buf.Reset()
	if _, err := Resolve(root, "4000001", "4000001"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("no warning expected with --group, got %q", buf.String())
	}
}

func TestResolveWarnsWhenANameMapsToRoot(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "etc", "passwd"), []byte("app:x:0:0::/:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	cred, err := Resolve(openRoot(t, rootDir), "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.UID != 0 || !strings.Contains(buf.String(), "maps to uid 0") {
		t.Fatalf("uid=%d, log=%q", cred.UID, buf.String())
	}
}

func TestResolveWithGetent(t *testing.T) {
	root := openRoot(t, newRoot(t))
	var calls []string
	orig := getent
	t.Cleanup(func() { getent = orig })
	getent = func(database, key string) ([]byte, error) {
		calls = append(calls, database+" "+key)
		switch database + " " + key {
		case "passwd ldap.user", "passwd 70001":
			return []byte("ldap.user:*:70001:70002:LDAP user:/home/ldap.user:/bin/bash\n"), nil
		case "passwd liar":
			return []byte("someone-else:*:70003:70003::/:/bin/sh\n"), nil
		case "group ldap-group":
			return []byte("ldap-group:*:70010:ldap.user\n"), nil
		case "group broken":
			return []byte("broken:*:x:\n"), nil
		}
		return nil, nil
	}

	tests := []struct {
		user, group string
		want        Credential
		wantErr     bool
	}{
		{user: "ldap.user", want: Credential{UID: 70001, GID: 70002, Groups: []int{70002}, Username: "ldap.user", Home: "/home/ldap.user"}},
		{user: "70001", want: Credential{UID: 70001, GID: 70002, Groups: []int{70002}, Username: "ldap.user", Home: "/home/ldap.user"}},
		{user: "ldap.user", group: "ldap-group", want: Credential{UID: 70001, GID: 70010, Groups: []int{70010}, Username: "ldap.user", Home: "/home/ldap.user"}},
		// The container's database still comes first.
		{user: "app", group: "ldap-group", want: Credential{UID: 1500, GID: 70010, Groups: []int{70010, 1700, 1800}, Username: "app", Home: "/home/app"}},
		{user: "liar", wantErr: true},
		{group: "broken", wantErr: true},
		{user: "-ldap.user", wantErr: true},
	}
	for _, tt := range tests {
		got, err := Resolve(root, tt.user, tt.group)
		if tt.wantErr {
			if err == nil {
				t.Errorf("--user %q --group %q: want error, got %+v", tt.user, tt.group, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("--user %q --group %q: %v", tt.user, tt.group, err)
			continue
		}
		if got.UID != tt.want.UID || got.GID != tt.want.GID || !slices.Equal(got.Groups, tt.want.Groups) ||
			got.Username != tt.want.Username || got.Home != tt.want.Home {
			t.Errorf("--user %q --group %q: got %+v, want %+v", tt.user, tt.group, *got, tt.want)
		}
	}
	if slices.Contains(calls, "passwd app") || slices.Contains(calls, "passwd -ldap.user") {
		t.Errorf("getent was called needlessly: %q", calls)
	}
}

func TestGetent(t *testing.T) {
	if _, err := os.Stat("/usr/bin/getent"); err != nil {
		t.Skip("getent is not installed")
	}
	out, err := getent("passwd", "root")
	if err != nil || !bytes.HasPrefix(out, []byte("root:")) {
		t.Fatalf("getent passwd root: %q, %v", out, err)
	}
	out, err = getent("group", "no-such-group-droot")
	if err != nil || out != nil {
		t.Fatalf("a missing key must give no output and no error: %q, %v", out, err)
	}
}
