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
