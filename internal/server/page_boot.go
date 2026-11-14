package server

// "Boot a kernel": the page that queues a job, reached from Jobs and from a
// Mac's page with that Mac chosen (/boot?mac=NAME). Who may boot what on
// which Mac is the Lease's rule and the Mac's Access level, asked by submit:
// the form only offers what would pass.

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /boot", web.MTeamUp, (*Server).pBoot)
	addPage("POST /boot", web.MTeamUp, (*Server).pBootIt)
}

// The three places a kernel comes from.
const (
	kernelArtifact = "artifact"
	kernelCurrent  = "current"
	kernelSource   = "source"
)

// The boot timeout the form offers, and what it takes.
const (
	bootTimeoutOffered = 240
	bootTimeoutMin     = 60
	bootTimeoutMax     = 1800
)

const crashSentence = "Crash test: the Mac panics on its Known-good kernel and restarts; anyone using it loses their session"

type builtinTest struct {
	name, about string
	gui         bool
}

// bootTests are the tests the form offers beside boot-health, which every
// job runs.
var bootTests = []builtinTest{
	{"gui-smoke", "Hyprland answers, a monitor is up, screenshot", true},
	{"gui-terminal", "open a terminal, type into it, screenshot", true},
	{"suspend-devices", "suspend and resume the devices, network back", false},
}

// bootForm is the form as it was sent, to say again beside a refusal.
type bootForm struct {
	Mac, Kernel, SHA, Source, Cmdline, Scripts, Timeout string
	Tests                                               []string
	ScriptsGUI                                          bool
}

type bootData struct {
	bootForm
	// Flash is a refusal, said above the form.
	Flash string
	// Maintainer adds the build from source and the Crash test.
	Maintainer bool
	Macs       []bootMac
	// NotOffered is why the Mac asked for is not among them.
	NotOffered string
	// GuideMac is the Mac the guide's commands name.
	GuideMac      string
	Offered       []bootTest
	BuildsClosed  string
	CrashSentence string
}

type bootMac struct {
	Name, About string
	Chosen      bool
}

type bootTest struct {
	Name, About string
	Checked     bool
}

func (s *Server) pBoot(w http.ResponseWriter, r *http.Request) {
	s.renderBoot(w, r, bootForm{Mac: r.URL.Query().Get("mac"), Kernel: kernelArtifact, Timeout: strconv.Itoa(bootTimeoutOffered)}, nil)
}

// noJobsOn is why the person queues no job on v now, or nil.
func (s *Server) noJobsOn(v *dev, p Person) error {
	d := v.snapshot()
	if d.State == api.StateLeft {
		return refuse(409, "%s left the lab", d.Name)
	}
	if err := s.fence(v, p, fenceJobs); err != nil {
		return err
	}
	switch {
	case d.KnownGood == "":
		return refuse(409, "%s has not passed its baseline", d.Name)
	case d.IsVolunteer() && v.windowClosed():
		return refuse(409, "%s is outside its Availability window", d.Name)
	}
	return nil
}

// renderBoot writes the page with the form as it stands; with a refusal, it
// is said above the form under its own status.
func (s *Server) renderBoot(w http.ResponseWriter, r *http.Request, form bootForm, refused error) {
	p := leaser(r)
	data := bootData{bootForm: form, Maintainer: p.isMaintainer(), BuildsClosed: teamBuildsClosed, CrashSentence: crashSentence}
	page := web.Page{Title: "Boot a kernel", Nav: "jobs"}
	if refused != nil {
		data.Flash, page.Status = refused.Error(), statusOf(refused)
	}
	devs := s.allDevs()
	sort.Slice(devs, func(i, j int) bool { return devs[i].snapshot().Name < devs[j].snapshot().Name })
	chosen := false
	for _, v := range devs {
		d := s.view(v)
		err := s.noJobsOn(v, p)
		if err != nil {
			if d.Name == form.Mac && d.State != api.StateLeft && refused == nil {
				data.NotOffered = err.Error()
			}
			continue
		}
		about := s.configuration(v).String() + " · Lab-owned"
		if d.IsVolunteer() {
			about = s.configuration(v).String() + " · a Volunteer's, Access level " + d.AccessLevel
		}
		data.Macs = append(data.Macs, bootMac{Name: d.Name, About: about, Chosen: d.Name == form.Mac})
		chosen = chosen || d.Name == form.Mac
	}
	if !chosen && len(data.Macs) > 0 {
		data.Macs[0].Chosen = true
	}
	data.GuideMac = "MAC"
	for _, m := range data.Macs {
		if m.Chosen {
			data.GuideMac = m.Name
		}
	}
	for _, t := range bootTests {
		data.Offered = append(data.Offered, bootTest{t.name, t.about, slices.Contains(form.Tests, t.name)})
	}
	page.Data = data
	s.render(w, r, "jobs/boot", page)
}

// spec is the job the form asks for, or why it is none.
func (f bootForm) spec(holder string) (api.JobSpec, error) {
	spec := api.JobSpec{Device: f.Mac, Holder: holder, Cmdline: f.Cmdline}
	if f.Mac == "" {
		return spec, refuse(400, "Choose a Mac.")
	}
	switch f.Kernel {
	case kernelArtifact:
		if !reSHA.MatchString(f.SHA) {
			return spec, refuse(400, "A kernel artifact is named by its sha256, 64 characters: `lab upload FILE` prints it.")
		}
		spec.Kernel = f.SHA
	case kernelSource:
		if f.Source == "" {
			return spec, refuse(400, "Paste the GitHub address of a repository, branch, commit or pull request.")
		}
		spec.Source = f.Source
	case kernelCurrent:
	default:
		return spec, refuse(400, "Say which kernel to boot.")
	}
	for _, name := range f.Tests {
		i := slices.IndexFunc(bootTests, func(t builtinTest) bool { return t.name == name })
		if i < 0 {
			return spec, refuse(400, "The lab has no test called %q.", name)
		}
		spec.Tests = append(spec.Tests, api.TestSpec{Name: name, Builtin: name, GUI: bootTests[i].gui})
	}
	for _, line := range strings.Split(f.Scripts, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		if len(words) != 2 || !reSHA.MatchString(words[1]) {
			return spec, refuse(400, "Each of your own scripts is one line: its name and the sha256 of the uploaded script.")
		}
		spec.Tests = append(spec.Tests, api.TestSpec{Name: words[0], Script: words[1], GUI: f.ScriptsGUI})
	}
	if f.Timeout != "" {
		sec, err := strconv.Atoi(f.Timeout)
		if err != nil || sec < bootTimeoutMin || sec > bootTimeoutMax {
			return spec, refuse(400, "The boot timeout is a number of seconds from %d to %d.", bootTimeoutMin, bootTimeoutMax)
		}
		spec.BootTimeoutSec = sec
	}
	return spec, nil
}

func (s *Server) pBootIt(w http.ResponseWriter, r *http.Request) {
	p := leaser(r)
	field := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	form := bootForm{Mac: field("mac"), Kernel: field("kernel"), SHA: field("sha"), Source: field("source"), Cmdline: field("cmdline"),
		Scripts: strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("scripts"), "\r", "")), Timeout: field("timeout"),
		ScriptsGUI: r.PostFormValue("scripts_gui") != ""}
	form.Tests = r.PostForm["test"]
	// A job queued on the site has the person's handle as its label, as a
	// Lease taken there has.
	var (
		spec api.JobSpec
		err  error
	)
	if r.PostFormValue("crash") != "" {
		switch {
		case !p.isMaintainer():
			err = refuse(403, "a Crash test is a Maintainer's")
		case form.Mac == "":
			err = refuse(400, "Choose a Mac.")
		}
		spec = api.JobSpec{Device: form.Mac, Holder: p.Handle, Crash: "panic"}
	} else {
		spec, err = form.spec(p.Handle)
	}
	if err == nil && s.dev(form.Mac) == nil {
		err = refuse(404, "The lab has no Mac called %q.", form.Mac)
	}
	if err != nil {
		s.renderBoot(w, r, form, err)
		return
	}
	if spec.Crash != "" && !s.sure(w, r, crashSentence, "/boot?mac="+url.QueryEscape(form.Mac)) {
		return
	}
	j, code, err := s.submit(p, spec, "")
	if err != nil {
		var refused *refusal
		if !errors.As(err, &refused) {
			err = refuse(code, "%v", err)
		}
		s.renderBoot(w, r, form, err)
		return
	}
	s.toJob(w, j.ID)
}
