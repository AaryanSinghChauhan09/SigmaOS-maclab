package reports

import (
	"regexp"
	"strings"
)

var (
	rePackageVersion = regexp.MustCompile(`\A(\d+(?:\.\d+)*)(.*?)(?:-[\d.]+)?\z`)
	rePrerelease     = regexp.MustCompile(`(?i)\A[.-]?(alpha|beta|rc)[.-]?(\d*)`)
	reGitSnapshot    = regexp.MustCompile(`\.r\d+\.g[0-9a-fA-F]+\z`)
)

// ReleaseOf is the release a package version names, coarse enough that two
// machines on the same release share it and saying when it isn't the release
// itself: "4.0.0.alpha.quattro.r179...-1" is 4.0.0-alpha, a snapshot built
// from git ("4.0.0.r7547.gf2b8378-1") 4.0.0-dev, "4.5.0rc2-1" 4.5.0-rc2.
func ReleaseOf(version string) string {
	found := rePackageVersion.FindStringSubmatch(version)
	if found == nil {
		return version
	}
	number, qualifier := found[1], found[2]
	if qualifier == "" {
		return number
	}
	if pre := rePrerelease.FindStringSubmatch(qualifier); pre != nil {
		return number + "-" + strings.ToLower(pre[1]) + pre[2]
	}
	if reGitSnapshot.MatchString(qualifier) {
		return number + "-dev"
	}
	return number + "-" + strings.TrimLeft(qualifier[:1], ".-") + qualifier[1:]
}

// The old site ordered releases and tool versions as RubyGems orders
// versions. gemVersion is that order: a "-" starts a pre-release, numbers
// compare as numbers, words as text, a word sorts below a number, and
// trailing zeros count for nothing.
type gemVersion []string

var reGemVersion = regexp.MustCompile(`\A[\s\v]*([0-9]+(\.[0-9a-zA-Z]+)*(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?)?[\s\v]*\z`)
var reGemSegment = regexp.MustCompile(`[0-9]+|[a-zA-Z]+`)

// gemVersionCorrect reports whether RubyGems takes text as a version.
func gemVersionCorrect(text string) bool { return reGemVersion.MatchString(text) }

// newGemVersion is the version text names, or version 0 when it names none.
func newGemVersion(text string) gemVersion {
	if !gemVersionCorrect(text) {
		text = "0"
	}
	text = strings.Trim(text, " \t\r\n\f\v\x00")
	if text == "" {
		text = "0"
	}
	segments := reGemSegment.FindAllString(strings.ReplaceAll(text, "-", ".pre."), -1)
	words := len(segments)
	for i, segment := range segments {
		if !numeral(segment) {
			words = i
			break
		}
	}
	trim := func(part []string) []string {
		for len(part) > 0 && zero(part[len(part)-1]) {
			part = part[:len(part)-1]
		}
		return part
	}
	return append(append(gemVersion{}, trim(segments[:words])...), trim(segments[words:])...)
}

func numeral(segment string) bool { return segment[0] >= '0' && segment[0] <= '9' }

func zero(segment string) bool { return numeral(segment) && strings.Trim(segment, "0") == "" }

func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return cmpInt(len(a), len(b))
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (v gemVersion) compare(other gemVersion) int {
	for i := 0; i < len(v) || i < len(other); i++ {
		a, b := "0", "0"
		if i < len(v) {
			a = v[i]
		}
		if i < len(other) {
			b = other[i]
		}
		switch {
		case numeral(a) && numeral(b):
			if c := compareNumbers(a, b); c != 0 {
				return c
			}
		case numeral(a):
			return 1
		case numeral(b):
			return -1
		case a != b:
			return strings.Compare(a, b)
		}
	}
	return 0
}
