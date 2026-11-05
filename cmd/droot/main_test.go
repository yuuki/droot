package main

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	gov := runtime.Version()
	vcs := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123"},
			{Key: "vcs.time", Value: "2026-11-05T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	tests := []struct {
		version, commit, date string
		bi                    *debug.BuildInfo
		want                  string
	}{
		{"v0.8.0", "abcdef0123456789", "2026-11-01T00:00:00Z", nil, "0.8.0 (commit abcdef012345, 2026-11-01T00:00:00Z, " + gov + ")"},
		{"", "", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.8.1"}}, "0.8.1 (" + gov + ")"},
		{"", "", "", vcs, "dev (commit 0123456789ab-dirty, 2026-11-05T10:00:00Z, " + gov + ")"},
		{"0.9.0", "", "", nil, "0.9.0 (" + gov + ")"},
	}
	for _, tt := range tests {
		if got := buildVersion(tt.version, tt.commit, tt.date, tt.bi); got != tt.want {
			t.Errorf("buildVersion(%q, %q, %q) = %q, want %q", tt.version, tt.commit, tt.date, got, tt.want)
		}
	}
}
