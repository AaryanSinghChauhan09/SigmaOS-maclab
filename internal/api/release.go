package api

import (
	"strconv"
	"strings"
)

// ReleaseOlder reports whether the Lab release have is older than the Lab
// release than, by their three numbers: a Release candidate counts as its
// release, with or without the tag's v. It is false when either is not a
// release, as a build from a checkout ("dev") is not.
func ReleaseOlder(have, than string) bool {
	h, ok := releaseNumbers(have)
	t, ok2 := releaseNumbers(than)
	if !ok || !ok2 {
		return false
	}
	for i := range h {
		if h[i] != t[i] {
			return h[i] < t[i]
		}
	}
	return false
}

func releaseNumbers(v string) (n [3]int, ok bool) {
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || strconv.Itoa(x) != p {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// VersionAtLeast compares lab-agent's dotted numbers: whether have is want
// or later. A valid build suffix ("+gui-workspace") does not count.
func VersionAtLeast(have, want string) bool {
	h, w := strings.Split(versionWithoutBuild(have), "."), strings.Split(versionWithoutBuild(want), ".")
	for i := range w {
		var a, b int
		if i < len(h) {
			a, _ = strconv.Atoi(h[i])
		}
		b, _ = strconv.Atoi(w[i])
		if a != b {
			return a > b
		}
	}
	return true
}

// Build identifiers do not affect version precedence. Strip only a valid
// metadata suffix, so malformed versions do not newly satisfy an agent gate.
func versionWithoutBuild(version string) string {
	core, build, found := strings.Cut(version, "+")
	if !found {
		return version
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return version
			}
		}
	}
	for _, identifier := range strings.Split(build, ".") {
		if identifier == "" {
			return version
		}
		for _, c := range identifier {
			if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '-') {
				return version
			}
		}
	}
	return core
}
