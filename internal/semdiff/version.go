package semdiff

import (
	"fmt"
	"strconv"
	"strings"
)

// Declared returns the bump a release tag pair claims. In 0.x, a minor bump is
// the breaking-change signal, so it counts as major.
func Declared(from, to string) (Level, error) {
	f, err := parseSemver(from)
	if err != nil {
		return 0, err
	}
	t, err := parseSemver(to)
	if err != nil {
		return 0, err
	}
	switch {
	case t[0] != f[0]:
		return Major, nil
	case t[1] != f[1] && f[0] == 0:
		return Major, nil
	case t[1] != f[1]:
		return Minor, nil
	case t[2] != f[2] && f[0] == 0:
		return Minor, nil
	default:
		return Patch, nil
	}
}

func parseSemver(v string) ([3]int, error) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-") // drop pre-release
	core, _, _ = strings.Cut(core, "+")                        // drop build metadata
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("%q is not semver (want vMAJOR.MINOR.PATCH)", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("%q is not semver", v)
		}
		out[i] = n
	}
	return out, nil
}
