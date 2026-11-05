package osutil

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Rlimit is a resource limit given with --ulimit NAME=SOFT[:HARD].
type Rlimit struct {
	Name       string
	Soft, Hard uint64
}

// RlimInfinity is the "unlimited" value of a resource limit.
const RlimInfinity = math.MaxUint64

// ParseRlimit parses NAME=SOFT[:HARD], where NAME is a resource as in
// `docker run --ulimit` (nofile, nproc, core, memlock, ...) and the limits
// are numbers, "unlimited" or -1. HARD defaults to SOFT.
func ParseRlimit(spec string) (Rlimit, error) {
	name, values, ok := strings.Cut(spec, "=")
	name = strings.ToLower(name)
	if !ok || name == "" || values == "" {
		return Rlimit{}, fmt.Errorf("invalid ulimit %q: want NAME=SOFT[:HARD]", spec)
	}
	if _, ok := rlimitResource(name); !ok {
		return Rlimit{}, fmt.Errorf("invalid ulimit %q: unknown resource %q (want one of %s)", spec, name, strings.Join(RlimitNames(), ", "))
	}
	softStr, hardStr, hasHard := strings.Cut(values, ":")
	if !hasHard {
		hardStr = softStr
	}
	soft, err1 := parseRlimitValue(softStr)
	hard, err2 := parseRlimitValue(hardStr)
	if err1 != nil || err2 != nil {
		return Rlimit{}, fmt.Errorf("invalid ulimit %q: limits must be numbers, unlimited or -1", spec)
	}
	if soft > hard {
		return Rlimit{}, fmt.Errorf("invalid ulimit %q: the soft limit is greater than the hard limit", spec)
	}
	return Rlimit{Name: name, Soft: soft, Hard: hard}, nil
}

func parseRlimitValue(s string) (uint64, error) {
	if s == "unlimited" || s == "-1" {
		return RlimInfinity, nil
	}
	return strconv.ParseUint(s, 10, 64)
}
