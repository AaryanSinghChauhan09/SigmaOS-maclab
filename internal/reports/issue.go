package reports

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// IssueDraft is a prefilled GitHub "new issue" link for a failure or a gap,
// which a Maintainer opens and files by hand: the lab never writes to
// GitHub. The target is the project that owns the finding's layer: Asahi
// hardware and Aurora additions go to the Aurora kernel (omacom/linux),
// Omarchy integration to omacom/omarchy-mac. The body links the reports
// behind the finding and, for a single check, its evidence, cut short so the
// link stays within what GitHub takes.
type IssueDraft struct {
	Repo, Title, Body string
}

const (
	kernelRepo            = "omacom/linux"
	IssueMaxBody          = 5000
	IssueMaxURL           = 7500 // GitHub refuses much longer new-issue links; percent-encoding can triple a body
	issueMaxReports       = 10
	issueMaxEvidenceLines = 20
)

var issueRepos = map[string]string{"asahi": kernelRepo, "aurora": kernelRepo, "omarchy": "omacom/omarchy-mac"}

var issueOutcomes = []string{"fails", "not-in-aurora", "not-in-asahi", "not-in-omarchy", "unknown-hardware"}

// Issues drafts issues for the published runs' findings. from names the lab
// in each body's last line; urlFor gives a report's link.
type Issues struct {
	pub    *Published
	from   string
	urlFor func(*Run) string
}

func (p *Published) Issues(from string, urlFor func(*Run) string) *Issues {
	return &Issues{pub: p, from: from, urlFor: urlFor}
}

func repoFor(layer string) string {
	if repo, ok := issueRepos[layer]; ok {
		return repo
	}
	return kernelRepo
}

// WorthAnIssue reports whether a check result is a failure or gap worth an issue.
func WorthAnIssue(check *Check) bool { return oneIn(issueOutcomes, check.Outcome) }

func on(run *Run) string {
	return run.ShortModel() + ", " + run.StackWords() + " " + run.OmarchyVersion()
}

func mac(run *Run) string {
	return fmt.Sprintf("%s, %s (%s, board %s)", run.Model(), run.Chip(), run.SoC(), run.Board())
}

func (i *Issues) featureLines(feature, layer string) []string {
	words := layer
	if known, ok := LayerWords[layer]; ok {
		words = known
	}
	return []string{fmt.Sprintf("- Feature: %s (`%s`), %s layer", i.pub.cat.FeatureName(feature), feature, words)}
}

func systemLines(run *Run) []string {
	stack := "- Stack: " + run.StackWords() + " " + run.OmarchyVersion()
	if set := run.CandidateSet(); set != "" {
		stack += ", candidate set " + set
	}
	kernel := "- Kernel: " + run.Kernel()
	if aurora := run.AuroraVersion(); aurora != "" {
		kernel += ", linux-aurora " + aurora
	}
	return []string{stack, kernel}
}

func expectedLine(expected *Object) []string {
	if expected == nil || len(expected.keys) == 0 {
		return nil
	}
	parts := make([]string, 0, len(expected.keys))
	for _, layer := range expected.keys {
		parts = append(parts, layer+" "+rubyText(expected.members[layer]))
	}
	return []string{"- Expected: " + strings.Join(parts, ", ")}
}

func (i *Issues) reportLines(heading string, runs []*Run) []string {
	shown := runs[:min(len(runs), issueMaxReports)]
	lines := []string{"", heading + ":"}
	for _, run := range shown {
		lines = append(lines, fmt.Sprintf("- %s (%s %s, %s)", i.urlFor(run), run.StackWords(), run.OmarchyVersion(), run.CreatedAt.UTC().Format("2006-01-02")))
	}
	if len(runs) > len(shown) {
		lines = append(lines, fmt.Sprintf("- and %d more", len(runs)-len(shown)))
	}
	return lines
}

func evidenceLines(evidence []string) []string {
	if len(evidence) == 0 {
		return nil
	}
	lines := []string{"", "Evidence:", "```text"}
	for _, line := range evidence[:min(len(evidence), issueMaxEvidenceLines)] {
		lines = append(lines, strings.ReplaceAll(line, "```", "'''"))
	}
	if len(evidence) > issueMaxEvidenceLines {
		lines = append(lines, fmt.Sprintf("(%d more lines in the report)", len(evidence)-issueMaxEvidenceLines))
	}
	return append(lines, "```")
}

func join(parts ...[]string) []string {
	var lines []string
	for _, part := range parts {
		lines = append(lines, part...)
	}
	return lines
}

// ForCheck drafts the issue of one check of a run.
func (i *Issues) ForCheck(run *Run, check *Check) *IssueDraft {
	cat := i.pub.cat
	var regressedFrom *Run
	if run.Regression(check.Feature) {
		regressedFrom = run.RegressedFrom(check.Feature)
	}
	outcome := cat.OutcomeWords(check.Outcome)
	title := cat.FeatureName(check.Feature) + ": " + outcome + " · " + on(run)
	first := fmt.Sprintf("`%s` on %s: **%s**.", check.ID, mac(run), outcome)
	var pass []string
	if regressedFrom != nil {
		title = "Regression: " + cat.FeatureName(check.Feature) + " · " + on(run)
		first = fmt.Sprintf("`%s` on %s: **%s** (a regression).", check.ID, mac(run), outcome)
		pass = []string{fmt.Sprintf("Last verified pass on the same model and stack: %s (%s)", i.urlFor(regressedFrom), regressedFrom.OmarchyVersion())}
	}
	return i.draft(check.Layer, title, join([]string{first, ""}, i.featureLines(check.Feature, check.Layer), systemLines(run), expectedLine(check.ExpectedStates()),
		[]string{"", "Report: " + i.urlFor(run)}, pass, evidenceLines(check.Evidence())))
}

// ForRegression drafts the issue of a regression still open.
func (i *Issues) ForRegression(regression OpenRegression) *IssueDraft {
	cat := i.pub.cat
	latest, pass := regression.Latest(), regression.Pass()
	layer := text(member(cat.Feature(regression.Feature), "layer"))
	name := cat.FeatureName(regression.Feature)
	return i.draft(layer, "Regression: "+name+" · "+on(latest), join(
		[]string{name + " stopped working on " + mac(latest) + ": an earlier Verified run on the same model and stack found it working.", ""},
		i.featureLines(regression.Feature, layer), systemLines(latest),
		[]string{"", fmt.Sprintf("Last verified pass: %s (%s %s, kernel %s)", i.urlFor(pass), pass.StackWords(), pass.OmarchyVersion(), pass.Kernel())},
		i.reportLines("Runs with the regression", regression.Runs)))
}

// ForReportedGap drafts the issue of a gap found on real Macs.
func (i *Issues) ForReportedGap(gap *ReportedGap) *IssueDraft {
	cat := i.pub.cat
	layer := text(member(cat.Feature(gap.Feature), "layer"))
	name, outcome := cat.FeatureName(gap.Feature), cat.OutcomeWords(gap.Outcome)
	return i.draft(layer, name+": "+outcome+" on "+gap.Chip, join(
		[]string{fmt.Sprintf("%s is %s on the %s (%s), found by %s: %s.", name, outcome, gap.Chip, gap.SoC, Plural(gap.Machines(), "machine"), strings.Join(gap.Models(), ", ")), ""},
		i.featureLines(gap.Feature, layer), i.reportLines("Reports", gap.Runs)))
}

// ForUnclaimed drafts the issue of hardware no driver claimed: the kernel's to claim.
func (i *Issues) ForUnclaimed(hardware *UnclaimedHardware) *IssueDraft {
	cat := i.pub.cat
	var chips, names []string
	for _, chip := range hardware.Chips() {
		chips = append(chips, chip[0]+" ("+chip[1]+")")
		if !oneIn(names, chip[0]) {
			names = append(names, chip[0])
		}
	}
	what := cat.OutcomeWords(hardware.Outcome)
	if hardware.Feature != "" {
		what = cat.FeatureName(hardware.Feature) + ": " + what
	}
	return i.draft("aurora", "No driver claims "+hardware.Compatible+" on "+strings.Join(names, ", "), join(
		[]string{fmt.Sprintf("No driver claims the enabled device-tree node `%s` on %s, found by %s: %s.", hardware.Compatible, strings.Join(chips, ", "),
			Plural(hardware.Machines(), "machine"), strings.Join(hardware.Models(), ", ")), "", "Feature catalogue: " + what + "."},
		i.reportLines("Reports", hardware.Runs)))
}

// ForCatalogueGap drafts the issue of a catalogue gap: missing (Aurora lacks
// what Asahi supports) or, when not, unverified (Aurora not qualified on it
// yet). featureURL is the feature's page.
func (i *Issues) ForCatalogueGap(gap CatalogueGap, missing bool, featureURL string) *IssueDraft {
	cat := i.pub.cat
	var chips []string
	for _, chip := range gap.Chips {
		chips = append(chips, cat.ChipName(chip))
	}
	where := strings.Join(chips, ", ")
	name := text(member(gap.Feature, "name"))
	title, first := name+": verify Aurora on "+where, "Asahi supports "+name+" on "+where+"; Aurora hasn't been qualified on it there yet."
	if missing {
		title, first = name+": supported by Asahi, not yet by Aurora ("+where+")", "Asahi supports "+name+" on "+where+"; the feature catalogue lists it as unsupported in Aurora."
	}
	return i.draft(layer(gap.Feature), title, join([]string{first, ""}, i.featureLines(id(gap.Feature), layer(gap.Feature)), []string{"Feature page: " + featureURL}))
}

func (i *Issues) draft(layer, title string, lines []string) *IssueDraft {
	d := &IssueDraft{Repo: repoFor(layer), Title: title}
	full := strings.Join(append(lines, "", fmt.Sprintf("From %s, feature catalogue v%d.", i.from, i.pub.cat.version)), "\n")
	d.Body = full
	length := utf8.RuneCountInString(full)
	for (utf8.RuneCountInString(d.Body) > IssueMaxBody || len(d.URL()) > IssueMaxURL) && length > 0 {
		length = min(length*9/10, IssueMaxBody-100)
		d.Body = cutShort(full, length)
	}
	return d
}

// URL is the new-issue link.
func (d *IssueDraft) URL() string {
	return "https://github.com/" + d.Repo + "/issues/new?" + url.Values{"title": {d.Title}, "body": {d.Body}}.Encode()
}

// cutShort is the body's first length characters, any open code fence closed.
func cutShort(body string, length int) string {
	kept := first(body, length)
	if strings.Count(kept, "```")%2 == 1 {
		kept += "\n```"
	}
	return kept + "\n\n(cut short; the linked reports have the rest)"
}
