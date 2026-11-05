package identity

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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
	root := newRoot(t)
	uid, gid := os.Getuid(), os.Getgid()

	tests := []struct {
		name        string
		user, group string
		want        Credential
		wantErr     bool
	}{
		{
			name: "no user and no group keeps the current identity",
			want: Credential{UID: uid, GID: gid},
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
	root := t.TempDir() // no /etc at all
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
	root := t.TempDir()
	got, err := Resolve(root, "root", "")
	if err != nil {
		t.Skipf("host has no root user: %v", err)
	}
	if got.UID != 0 || got.Username != "root" {
		t.Fatalf("unexpected credential %+v", *got)
	}
}
