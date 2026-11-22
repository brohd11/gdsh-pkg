package gdshpkg

import (
	"strconv"
	"strings"
)

// CompareVersions orders "v1.2.3"-style versions: -1, 0 or 1. A leading v is ignored, numeric
// parts compare as numbers, and a missing part counts as 0.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := "0", "0"
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil:
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
		case x != y:
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
}
