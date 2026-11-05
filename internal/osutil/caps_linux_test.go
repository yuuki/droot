package osutil

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestCapabilityNames(t *testing.T) {
	for name, want := range map[string]uintptr{
		"CHOWN": unix.CAP_CHOWN, "cap_net_raw": unix.CAP_NET_RAW, "CAP_SYS_ADMIN": unix.CAP_SYS_ADMIN,
		"MKNOD": unix.CAP_MKNOD, "Audit_Read": unix.CAP_AUDIT_READ, "CHECKPOINT_RESTORE": unix.CAP_CHECKPOINT_RESTORE,
	} {
		if got, ok := Capability(name); !ok || got != want {
			t.Errorf("Capability(%q) = %d, %v; want %d", name, got, ok, want)
		}
	}
	if _, ok := Capability("NOPE"); ok {
		t.Error("unknown names must be refused")
	}
	if got := CapabilityName(unix.CAP_NET_BIND_SERVICE); got != "CAP_NET_BIND_SERVICE" {
		t.Errorf("CapabilityName = %q", got)
	}
	if n := len(AllCapabilities()); n != unix.CAP_LAST_CAP+1 {
		t.Errorf("%d capabilities, the kernel headers know %d", n, unix.CAP_LAST_CAP+1)
	}
}
