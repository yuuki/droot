package log

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLog(t *testing.T) {
	var b strings.Builder
	SetOutput(&b)
	t.Cleanup(func() {
		SetDebug(false)
		SetQuiet(false)
		_ = SetFormat("text")
	})

	Debugf("hidden")
	Infof("info %d", 1)
	Warnf("careful\n")
	Errorf("failed")
	SetDebug(true)
	Debugf("shown")
	if want := "info 1\nwarning: careful\ndroot: failed\n[debug] shown\n"; b.String() != want {
		t.Fatalf("text:\n%q\nwant\n%q", b.String(), want)
	}

	b.Reset()
	SetQuiet(true)
	Infof("hidden")
	Warnf("hidden")
	Errorf("error")
	if b.String() != "droot: error\n" || !IsQuiet() {
		t.Fatalf("quiet: %q", b.String())
	}

	b.Reset()
	SetQuiet(false)
	if err := SetFormat("json"); err != nil || !IsJSON() {
		t.Fatal(err)
	}
	Warnf("disk %s", "full")
	Errorf("failed")
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("json: %q", b.String())
	}
	for i, want := range []struct{ level, msg string }{{"WARN", "disk full"}, {"ERROR", "failed"}} {
		var rec map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
			t.Fatal(err)
		}
		if rec["level"] != want.level || rec["msg"] != want.msg || rec["time"] == nil {
			t.Errorf("record %d: %v", i, rec)
		}
	}
	if err := SetFormat("xml"); err == nil {
		t.Error("unknown formats must be refused")
	}
}
