package systemd

import (
	"os"
	"reflect"
	"strconv"
	"testing"
)

func lookupIn(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"none", map[string]string{"HOME": "/root"}, nil},
		{"notify", map[string]string{"NOTIFY_SOCKET": "/run/systemd/notify", "HOME": "/root"},
			[]string{"NOTIFY_SOCKET=/run/systemd/notify"}},
		{"sockets of droot",
			map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "2", "LISTEN_FDNAMES": "web:admin", "LISTEN_PIDFDID": "1234"},
			[]string{"LISTEN_PID=42", "LISTEN_PIDFDID=1234", "LISTEN_FDS=2", "LISTEN_FDNAMES=web:admin"}},
		{"sockets of another process", map[string]string{"LISTEN_PID": "41", "LISTEN_FDS": "2", "LISTEN_FDNAMES": "web:admin"}, nil},
		{"sockets without LISTEN_PID", map[string]string{"LISTEN_FDS": "2"}, nil},
		{"invalid LISTEN_PID", map[string]string{"LISTEN_PID": "x42", "LISTEN_FDS": "2"}, nil},
		{"watchdog of droot", map[string]string{"WATCHDOG_USEC": "30000000", "WATCHDOG_PID": "42"},
			[]string{"WATCHDOG_USEC=30000000", "WATCHDOG_PID=42"}},
		{"watchdog of any process", map[string]string{"WATCHDOG_USEC": "30000000"}, []string{"WATCHDOG_USEC=30000000"}},
		{"watchdog of another process", map[string]string{"WATCHDOG_USEC": "30000000", "WATCHDOG_PID": "1"}, nil},
		{"WATCHDOG_PID alone", map[string]string{"WATCHDOG_PID": "42"}, nil},
		{"everything",
			map[string]string{"NOTIFY_SOCKET": "@notify", "LISTEN_PID": "42", "LISTEN_FDS": "1", "WATCHDOG_USEC": "1", "WATCHDOG_PID": "42"},
			[]string{"NOTIFY_SOCKET=@notify", "LISTEN_PID=42", "LISTEN_FDS=1", "WATCHDOG_USEC=1", "WATCHDOG_PID=42"}},
	}
	for _, tt := range tests {
		if got := Env(lookupIn(tt.env), 42); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestListenFDs(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want int
	}{
		{map[string]string{}, 0},
		{map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "3"}, 3},
		{map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "0"}, 0},
		{map[string]string{"LISTEN_PID": "41", "LISTEN_FDS": "3"}, 0},
		{map[string]string{"LISTEN_FDS": "3"}, 0},
		{map[string]string{"LISTEN_PID": "42"}, 0},
		{map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "-1"}, 0},
		{map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "three"}, 0},
		{map[string]string{"LISTEN_PID": "42", "LISTEN_FDS": "99999999999"}, 0},
	}
	for _, tt := range tests {
		if got := ListenFDs(lookupIn(tt.env), 42); got != tt.want {
			t.Errorf("ListenFDs(%v) = %d, want %d", tt.env, got, tt.want)
		}
	}
}

func TestAdopt(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	parent := os.Getpid() + 1
	t.Setenv("LISTEN_PID", strconv.Itoa(parent))
	t.Setenv("LISTEN_PIDFDID", "1234")
	t.Setenv("LISTEN_FDS", "2")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(parent))
	Adopt(parent)
	if v := os.Getenv("LISTEN_PID"); v != self {
		t.Errorf("LISTEN_PID=%s, want %s", v, self)
	}
	if v, ok := os.LookupEnv("LISTEN_PIDFDID"); ok {
		t.Errorf("LISTEN_PIDFDID=%s is still set", v)
	}
	if v := os.Getenv("WATCHDOG_PID"); v != self {
		t.Errorf("WATCHDOG_PID=%s, want %s", v, self)
	}
	if v := os.Getenv("LISTEN_FDS"); v != "2" {
		t.Errorf("LISTEN_FDS=%s, want 2", v)
	}

	// Those of another process are left alone.
	t.Setenv("LISTEN_PID", "1")
	t.Setenv("LISTEN_PIDFDID", "1234")
	t.Setenv("WATCHDOG_PID", "1")
	Adopt(parent)
	if os.Getenv("LISTEN_PID") != "1" || os.Getenv("LISTEN_PIDFDID") != "1234" || os.Getenv("WATCHDOG_PID") != "1" {
		t.Errorf("the variables of another process were changed: LISTEN_PID=%s LISTEN_PIDFDID=%s WATCHDOG_PID=%s",
			os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_PIDFDID"), os.Getenv("WATCHDOG_PID"))
	}
}

func TestNotifySocketPath(t *testing.T) {
	for addr, want := range map[string]string{
		"/run/systemd/notify":           "/run/systemd/notify",
		"/run/user/1000/systemd/notify": "/run/user/1000/systemd/notify",
		"@/org/freedesktop/notify":      "",
		"vsock:2:1234":                  "",
		"vsock-stream:2:1234":           "",
		"relative/notify":               "",
		"":                              "",
	} {
		if got := NotifySocketPath(addr); got != want {
			t.Errorf("NotifySocketPath(%q) = %q, want %q", addr, got, want)
		}
	}
}
