package reports

import (
	"regexp"
	"sort"
	"strings"
)

// Build is the build a run is on, derived from what its report records, the
// way omarchy-m-test derives it (cli/omarchy_m_test/system.py, build_words):
//
//	ID       the Omarchy runtime's commit (7 characters, from the omarchy
//	         package's ".g<commit>") and its build stamp (the pkgrel suffix the
//	         candidate-set lane gives every package of one build, "-1.2026092602"),
//	         e.g. "f60e1ba.2026092602"; a release package's own version when it
//	         has neither
//	Kernel   the linux-aurora version, less the build's stamp
//	Boot     the omarchy-mac-boot version, less the build's stamp
//
// An image that names its candidate set is labelled by the set, with the
// runtime build, when the image was built and its package set digest after
// it. Two runs are on the same build when all of these match: the same
// runtime with a kernel installed by hand is a different build to look at.
// Reference runs (another distro) have none.
type Build struct {
	ID, Commit, Stamp, Kernel, Boot string
	// CandidateSet is the set the image names, "" when it names none.
	CandidateSet string
	// Image is the image's record, in the order the CLI reads its keys.
	Image       []ImageFact
	ToolVersion string

	omarchy string
}

// ImageFact is one line of an image's record.
type ImageFact struct{ Key, Value string }

var runtimePackages = []string{"omarchy", "omarchy-settings", "omarchy-dev"}

// imageKeys are system.image's keys in the order the CLI reads them.
var imageKeys = []string{"format", "platform", "candidate_set", "candidate_source_commit", "builder_commit", "builder_tree_clean",
	"image_profile", "package_set_sha256", "built"}

// shortFacts is how long a commit or digest reads on a page; the exports keep them whole.
var shortFacts = map[string]int{"candidate_source_commit": 12, "builder_commit": 12, "package_set_sha256": 12}

var (
	reCommitRun = regexp.MustCompile(`\.g([0-9a-f]+)`)
	reStamp     = regexp.MustCompile(`-\d+\.(\d+)\z`)
)

// commitOf is the commit a package version carries after ".g": 7 to 40 hex
// digits that no further hex digit follows.
func commitOf(version string) string {
	for _, found := range reCommitRun.FindAllStringSubmatch(version, -1) {
		if n := len(found[1]); n >= 7 && n <= 40 {
			return found[1]
		}
	}
	return ""
}

func first(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func buildFor(run *Run) *Build {
	if run.Stack() == "reference" {
		return nil
	}
	packages := map[string]string{}
	for _, pkg := range run.packages() {
		packages[text(member(asObject(pkg), "name"))] = text(member(asObject(pkg), "version"))
	}
	var image []ImageFact
	if record, _ := member(run.doc, "system", "image").(*Object); record != nil {
		for _, key := range record.keys {
			image = append(image, ImageFact{key, text(record.members[key])})
		}
	}
	set := run.CandidateSet()
	if set == "" {
		for _, fact := range image {
			if fact.Key == "candidate_set" {
				set = fact.Value
			}
		}
	}
	build := NewBuild(packages, set, image, run.ToolVersion())
	if strings.TrimSpace(build.ID) == "" {
		return nil
	}
	return build
}

// NewBuild is the build of a set of packages (name to version) and an
// image's record.
func NewBuild(packages map[string]string, candidateSet string, image []ImageFact, toolVersion string) *Build {
	rank := func(key string) int {
		for i, known := range imageKeys {
			if known == key {
				return i
			}
		}
		return len(imageKeys)
	}
	image = append([]ImageFact(nil), image...)
	sort.SliceStable(image, func(i, j int) bool {
		if a, b := rank(image[i].Key), rank(image[j].Key); a != b {
			return a < b
		}
		return image[i].Key < image[j].Key
	})
	b := &Build{CandidateSet: candidateSet, Image: image, ToolVersion: toolVersion, omarchy: packages["omarchy"]}
	for _, name := range runtimePackages {
		if version := packages[name]; version != "" {
			b.Commit = first(commitOf(version), 7)
			break
		}
	}
	versioned := append(append([]string(nil), runtimePackages...), "omarchy-mac")
	for _, name := range versioned {
		if found := reStamp.FindStringSubmatch(packages[name]); found != nil {
			b.Stamp = found[1]
			break
		}
	}
	switch {
	case b.Commit != "" && b.Stamp != "":
		b.ID = b.Commit + "." + b.Stamp
	case b.Commit != "" || b.Stamp != "":
		b.ID = b.Commit + b.Stamp
	default:
		for _, name := range versioned {
			if version := packages[name]; version != "" {
				b.ID = version
				break
			}
		}
	}
	b.Kernel, b.Boot = b.unstamped(packages["linux-aurora"]), b.unstamped(packages["omarchy-mac-boot"])
	return b
}

func (b *Build) unstamped(version string) string {
	if b.Stamp == "" {
		return version
	}
	return strings.TrimSuffix(version, "."+b.Stamp)
}

func (b *Build) fact(key string) string {
	for _, fact := range b.Image {
		if fact.Key == key {
			return fact.Value
		}
	}
	return ""
}

// Label is what the build is called: the candidate set the image names, else the runtime build.
func (b *Build) Label() string {
	if b.CandidateSet != "" {
		return b.CandidateSet
	}
	return b.ID
}

// Built is when the image was built, "" when its record doesn't say.
func (b *Build) Built() string { return b.fact("built") }

// Words is "f60e1ba.2026092602 (linux-aurora 7.1.12.aurora2-11,
// omarchy-mac-boot 20260926-1)", or on an image naming its set
// "apple-test-f22c43fb7903-20260928 (runtime f22c43f.…, linux-aurora …,
// omarchy-mac-boot …, built …)": the CLI shows the same. It is also what two
// runs share when they're on the same build.
func (b *Build) Words() string {
	var extras []string
	if b.CandidateSet != "" {
		extras = append(extras, "runtime "+b.ID)
	}
	if b.Kernel != "" {
		extras = append(extras, "linux-aurora "+b.Kernel)
	}
	if b.Boot != "" {
		extras = append(extras, "omarchy-mac-boot "+b.Boot)
	}
	if built := b.Built(); built != "" {
		extras = append(extras, "built "+built)
	}
	if digest := b.fact("package_set_sha256"); digest != "" {
		extras = append(extras, "package set "+first(digest, 12))
	}
	if len(extras) == 0 {
		return b.Label()
	}
	return b.Label() + " (" + strings.Join(extras, ", ") + ")"
}

// Matches reports whether ?build= names this build: its runtime build id or
// label (every variant of it), or its words (exactly this one).
func (b *Build) Matches(name string) bool {
	return strings.TrimSpace(name) != "" && (name == b.ID || name == b.Label() || name == b.Words())
}

// ImageWords is the image record as it reads on a page, commits and digests shortened.
func (b *Build) ImageWords() string {
	parts := make([]string, 0, len(b.Image))
	for _, fact := range b.Image {
		value := fact.Value
		if n, short := shortFacts[fact.Key]; short {
			value = first(value, n)
		}
		parts = append(parts, fact.Key+" "+value)
	}
	return strings.Join(parts, ", ")
}

// MatchedSet is the known candidate set built with this omarchy package,
// when the image doesn't name its set: matched, shown, but never counted
// towards the set's promotion.
func (b *Build) MatchedSet() string {
	if b.CandidateSet != "" {
		return ""
	}
	return knownSetFor(b.omarchy)
}

// Set is the candidate set the build is on, named by its image or matched by its packages.
func (b *Build) Set() string {
	if b.CandidateSet != "" {
		return b.CandidateSet
	}
	return b.MatchedSet()
}

// SetSource is how the set is known: "image" names it, or its "packages" match a known set.
func (b *Build) SetSource() string {
	switch {
	case b.CandidateSet != "":
		return "image"
	case b.MatchedSet() != "":
		return "packages"
	}
	return ""
}

// BuildRuns is one build and the runs on it, newest first.
type BuildRuns struct {
	Build *Build
	Runs  []*Run
}

// RunsByBuild gives the runs on each build, newest build first. Reference runs have none.
func RunsByBuild(runs []*Run) []BuildRuns {
	sorted := append([]*Run(nil), runs...)
	sortRuns(sorted)
	var out []BuildRuns
	at := map[string]int{}
	for _, run := range newestFirst(sorted) {
		build := run.Build()
		if build == nil {
			continue
		}
		i, seen := at[build.Words()]
		if !seen {
			i = len(out)
			at[build.Words()] = i
			out = append(out, BuildRuns{Build: build})
		}
		out[i].Runs = append(out[i].Runs, run)
	}
	return out
}
