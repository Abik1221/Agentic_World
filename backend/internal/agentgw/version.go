package agentgw

import (
	"strconv"
	"strings"
)

// versionLess reports whether dotted version a is strictly older than b
// (numeric field-by-field, e.g. "1.2.0" < "1.10.0"). It is used only to decide
// whether to refuse a too-old SDK, so it fails OPEN: if either version can't be
// parsed as dotted integers, it returns false (do not refuse). Pre-release/build
// suffixes are ignored (compared on the numeric core only).
func versionLess(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

// parseVersion parses "MAJOR.MINOR.PATCH" (missing trailing parts default to 0,
// any pre-release/build suffix on the last field is dropped) into a [3]int.
func parseVersion(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		// Drop a "-rc1"/"+build" style suffix on this field.
		if cut := strings.IndexAny(part, "-+"); cut >= 0 {
			part = part[:cut]
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
