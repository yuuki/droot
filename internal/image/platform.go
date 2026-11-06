package image

import (
	"fmt"
	"runtime"
	"strings"
)

// Platform is the platform of an image.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// ParsePlatform parses OS/ARCH[/VARIANT]. An empty string gives the
// platform droot runs on.
func ParsePlatform(s string) (Platform, error) {
	if s == "" {
		p := Platform{OS: "linux", Architecture: runtime.GOARCH}
		if p.Architecture == "arm" {
			p.Variant = "v7" // the release binaries are built for ARMv7
		}
		return p, nil
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] == "") {
		return Platform{}, fmt.Errorf("invalid platform %q: want OS/ARCH[/VARIANT], e.g. linux/arm64", s)
	}
	p := Platform{OS: parts[0], Architecture: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return p, nil
}

// normalize gives the canonical form of a platform, as containerd does:
// amd64 and arm64 without variant (for v1 and v8), arm with one (v7 by
// default).
func (p Platform) normalize() Platform {
	p.OS = strings.ToLower(p.OS)
	p.Architecture = strings.ToLower(p.Architecture)
	p.Variant = strings.ToLower(p.Variant)
	switch p.Architecture {
	case "x86_64", "x86-64", "amd64":
		p.Architecture = "amd64"
		if p.Variant == "v1" {
			p.Variant = ""
		}
	case "aarch64", "arm64":
		p.Architecture = "arm64"
		if p.Variant == "v8" || p.Variant == "8" {
			p.Variant = ""
		}
	case "armhf":
		p.Architecture, p.Variant = "arm", "v7"
	case "armel":
		p.Architecture, p.Variant = "arm", "v6"
	case "arm":
		switch p.Variant {
		case "":
			p.Variant = "v7"
		case "5", "6", "7", "8":
			p.Variant = "v" + p.Variant
		}
	case "i386", "i486", "i586", "i686", "x86":
		p.Architecture = "386"
	}
	return p
}

// score tells how well an image for platform have suits p: 0 if it does not
// run on p, more the better.
func (p Platform) score(have Platform) int {
	want, have := p.normalize(), have.normalize()
	if want.OS != have.OS || want.Architecture != have.Architecture {
		return 0
	}
	if want.Variant == have.Variant {
		return 100
	}
	// An ARM CPU runs the code of older versions of the architecture.
	if want.Architecture == "arm" && len(want.Variant) == 2 && len(have.Variant) == 2 &&
		want.Variant[0] == 'v' && have.Variant[0] == 'v' && have.Variant[1] < want.Variant[1] {
		return 10 + int(have.Variant[1]-'0')
	}
	return 0
}
