package server

// A Compare's report on the site: on its job's page for Maintainers, with
// Publish and, while it waits for a person, Done; once published, on its
// own public page /compares/{id} (not kept by the edge).

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /compares/{id}", web.Anyone, (*Server).pCompare)
	addPage("POST /compares/{id}/publish", web.Maintainers, (*Server).pComparePublish)
	addPage("POST /compares/{id}/done", web.SignedIn, (*Server).pCompareDone)
}

type compareView struct {
	ID, Before, After, Tier string
	Sides                   []compareSideView
	Rows                    []compareRowView
	MacOS                   bool
	Checks                  []api.CompareCheck // the checks that are not a plain pass
	Regressed, Fixed        int
	Published, Hands        string
	// CanPublish and Waiting are for a Maintainer on the job's page.
	CanPublish, Waiting bool
}

type compareSideView struct {
	Side, Set, Boot, Kernel, BootIn            string
	GPUFirmware, SIO, Thunderbolt, FailedUnits string
	OMT, Error, Journal                        string
	Packages                                   []api.CandidatePackage
}

type compareRowView struct {
	Name, Unit, A, B, Delta, Verdict, Class, Noise, Note string
	MacOS, MacOSDelta, MacOSVerdict, MacOSClass          string
}

var verdictClass = map[string]string{api.VerdictBetter: "job-ok", api.VerdictWorse: "job-bad", api.VerdictNoChange: "dim", api.VerdictNotCompared: "dim"}

func num(p *float64) string {
	if p == nil {
		return "-"
	}
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", *p), "0"), ".")
}

func pct(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%+.1f%%", *p)
}

func compareViewOf(j *api.Job) *compareView {
	res := j.Result.Compare
	if j.Spec.Compare == nil || res == nil {
		return nil
	}
	cv := &compareView{ID: j.ID, Before: res.Before, After: res.After, Tier: res.Tier, Regressed: res.Regressed, Fixed: res.Fixed}
	for _, s := range res.Sides {
		sv := compareSideView{Side: s.Side, Set: s.Set, Boot: s.Boot, Kernel: s.Kernel, GPUFirmware: orStr(s.GPUFirmware, "-"),
			SIO: orStr(s.SIO, "-"), Thunderbolt: orStr(s.Thunderbolt, "-"), FailedUnits: orStr(strings.Join(s.FailedUnits, " "), "none"),
			Error: s.Error, Packages: s.Packages}
		if s.BootSeconds > 0 {
			sv.BootIn = fmt.Sprintf("%.0f s", s.BootSeconds)
		}
		if s.OMT != nil {
			sv.OMT = fmt.Sprintf("%s: %d pass, %d fail, %d skipped", s.OMT.Tool, s.OMT.Pass, s.OMT.Fail, s.OMT.Skip)
		}
		if lines := strings.Split(strings.TrimSpace(s.Journal), "\n"); s.Journal != "" {
			sv.Journal = strings.Join(lines[max(0, len(lines)-40):], "\n")
		}
		cv.MacOS = cv.MacOS || s.Side == api.SideMacOS
		cv.Sides = append(cv.Sides, sv)
	}
	for _, r := range res.Rows {
		rv := compareRowView{Name: r.Name, Unit: r.Unit, A: num(r.A), B: num(r.B), Delta: pct(r.Delta), Verdict: r.Verdict,
			Class: verdictClass[r.Verdict], Noise: fmt.Sprintf("±%.1f%% (%s)", r.Noise, r.NoiseFrom), Note: r.Note,
			MacOS: num(r.MacOS), MacOSDelta: pct(r.MacOSDelta), MacOSVerdict: r.MacOSVerdict, MacOSClass: verdictClass[r.MacOSVerdict]}
		if r.MacOSNote != "" {
			rv.MacOSVerdict = r.MacOSNote
		}
		if !r.HigherIsBetter && r.Unit != "" {
			rv.Unit += ", lower is better"
		}
		cv.Rows = append(cv.Rows, rv)
	}
	for _, c := range res.Checks {
		if c.Change != api.CheckPass {
			cv.Checks = append(cv.Checks, c)
		}
	}
	if res.Published != nil {
		cv.Published = when(*res.Published) + " by @" + res.PublishedBy
	}
	if h := res.Hands; h != nil {
		switch {
		case h.Waiting:
			cv.Hands, cv.Waiting = "waiting for a person at the Mac", true
		case h.Done != nil:
			cv.Hands = "done by @" + h.By + " " + when(*h.Done)
		default:
			cv.Hands = orStr(h.Note, "asked for")
		}
	}
	return cv
}

func (s *Server) pCompare(w http.ResponseWriter, r *http.Request) {
	j, err := s.store.job(r.PathValue("id"))
	if err != nil || j.Spec.Compare == nil || comparePrivate(j) {
		s.notFound(w, r)
		return
	}
	s.render(w, r, "compare", web.Page{Title: "Compare " + j.Spec.Compare.Before + " and " + j.Spec.Compare.After, Data: struct {
		Mac string
		C   *compareView
	}{j.Spec.Device, compareViewOf(j)}})
}

func (s *Server) pComparePublish(w http.ResponseWriter, r *http.Request) {
	j, p := s.jobOr404(w, r)
	if j == nil {
		return
	}
	if _, err := s.publishCompare(j.ID, p); err != nil {
		s.renderJob(w, r, j, p, err)
		return
	}
	s.toJob(w, j.ID)
}

// pCompareDone is the Done button, on the Mac's page, of a Compare waiting
// for a person: a Maintainer's, or the Mac's own Volunteer's.
func (s *Server) pCompareDone(w http.ResponseWriter, r *http.Request) {
	p := leaser(r)
	j, err := s.store.job(r.PathValue("id"))
	if err != nil || j.Spec.Compare == nil || !s.mayHands(p, j) {
		s.notFound(w, r)
		return
	}
	if err := s.compareDone(j.ID, p); err != nil {
		if v := s.dev(j.Spec.Device); v != nil {
			s.renderMac(w, r, v, tabOverview, err)
			return
		}
	}
	w.Header().Set("Location", s.basePath()+"/macs/"+j.Spec.Device)
	w.WriteHeader(http.StatusSeeOther)
}
