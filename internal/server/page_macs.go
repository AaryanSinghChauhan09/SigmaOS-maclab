package server

// Macs: the fleet with who holds each Lease, for the M team and the
// Maintainers. Lease, Renew and Release are its buttons, and a row that
// takes no Lease says why. A Maintainer sets a Mac's Configuration here.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /macs", web.MTeamUp, (*Server).pMacs)
	addPage("POST /macs/{name}/lease", web.MTeamUp, (*Server).pMacsLease)
	addPage("POST /macs/{name}/renew", web.MTeamUp, (*Server).pMacsRenew)
	addPage("POST /macs/{name}/release", web.MTeamUp, (*Server).pMacsRelease)
	addPage("POST /macs/{name}/configuration", web.Maintainers, (*Server).pMacsConfiguration)
}

// macsShow are the filters of Macs.
const (
	showAll    = "all"
	showMine   = "mine"
	showLeased = "leased"
)

type macsData struct {
	Show string
	// Flash is a refusal, said above the table.
	Flash string
	// Maintainer adds the control that sets a Configuration, and the hours
	// of a Lease.
	Maintainer bool
	Macs       []macRow
	// Empty is said in place of the table.
	Empty string
}

type macRow struct {
	Name string
	// Kind is whose Mac it is in words; Volunteer its Volunteer's handle,
	// when the lab knows it.
	Kind, Volunteer string
	Configuration   string
	// CPU and GPU are the counts a Maintainer set, for the control.
	CPU, GPU string
	SetBy    string
	State    string
	// Holder is who holds its Lease, HolderKind their kind and Until its end.
	Holder, HolderKind, HolderClass, Until string
	// CanLease, or Mine: the viewer holds its Lease, and renews or releases.
	CanLease, Mine bool
	// Why is why the row takes no Lease; Warning what a Maintainer is told
	// beside a Lease they may still take.
	Why, Warning string
}

func showOf(raw string) string {
	if raw == showMine || raw == showLeased {
		return raw
	}
	return showAll
}

// macsPath is Macs under a filter.
func macsPath(show string) string {
	if show == showAll {
		return "/macs"
	}
	return "/macs?show=" + show
}

func (s *Server) pMacs(w http.ResponseWriter, r *http.Request) {
	s.renderMacs(w, r, showOf(r.URL.Query().Get("show")), nil)
}

// renderMacs writes Macs; with a refusal, it is said above the table under
// its own status.
func (s *Server) renderMacs(w http.ResponseWriter, r *http.Request, show string, refused error) {
	p := leaser(r)
	data := macsData{Show: show, Maintainer: p.isMaintainer()}
	page := web.Page{Title: "Macs", Nav: "macs"}
	if refused != nil {
		data.Flash, page.Status = refused.Error(), statusOf(refused)
	}
	type mac struct {
		v *dev
		d DeviceView
	}
	var macs []mac
	for _, v := range s.allDevs() {
		macs = append(macs, mac{v, s.view(v)})
	}
	sort.Slice(macs, func(i, j int) bool { return macs[i].d.Name < macs[j].d.Name })
	for _, m := range macs {
		v, d := m.v, m.d
		if d.State == api.StateLeft {
			continue
		}
		mine := s.holdsLease(v, p)
		if show == showLeased && !mine || show == showMine && (d.VolunteerID == 0 || d.VolunteerID != p.AccountID) {
			continue
		}
		data.Macs = append(data.Macs, s.macRow(v, d, p, mine))
	}
	if len(data.Macs) == 0 {
		data.Empty = map[string]string{showAll: "No Mac is enrolled.", showMine: "You lend no Mac.", showLeased: "You hold no Lease."}[show]
	}
	page.Data = data
	s.render(w, r, "macs", page)
}

func (s *Server) macRow(v *dev, d DeviceView, p Person, mine bool) macRow {
	row := macRow{Name: d.Name, Kind: "Lab-owned", State: s.macState(v, d), Mine: mine}
	if d.IsVolunteer() {
		row.Kind = "Volunteer's"
		if volunteer, err := s.person(d.VolunteerID); err == nil {
			row.Volunteer = volunteer.Handle
		}
	}
	conf := s.configuration(v)
	row.Configuration, row.SetBy = conf.String(), conf.SetBy
	if conf.SetBy != "" {
		row.CPU, row.GPU = strconv.Itoa(conf.CPU), strconv.Itoa(conf.GPU)
	}
	if l := d.Lease; l != nil {
		row.Holder, row.Until = orStr(l.Owner(), l.Holder), when(l.Expires)
		if l.Extended {
			row.Until += ", extended until its job ends"
		}
		if holder, err := s.person(l.AccountID); err == nil {
			// Who is an Admin is on no screen: here an Admin is a Maintainer.
			kind := min(holder.Role(), web.Maintainer)
			row.Holder, row.HolderKind, row.HolderClass = holder.Handle, kind.Badge(), kind.BadgeClass()
		}
	}
	if mine {
		return row
	}
	found, err := s.mayLease(v, p, p.Handle)
	switch {
	case err == nil:
		row.CanLease = true
		if held, _ := s.heldOfConfiguration(v, p); found.warning != "" && held != "" {
			row.Warning = "you hold " + held + ", the same Configuration: an M team member would be refused"
		}
	case d.Lease != nil:
		row.Why = "leased by @" + row.Holder + " until " + row.Until
	case d.KnownGood == "":
		row.Why = "has not passed its baseline"
	case d.IsVolunteer() && v.windowClosed():
		row.Why = "outside its Availability window"
	default:
		row.Why = err.Error()
		if held, _ := s.heldOfConfiguration(v, p); held != "" {
			row.Why = "you hold " + held + ", the same Configuration"
			if !conf.Known() || !s.configuration(s.dev(held)).Known() {
				row.Why = "you hold " + held + ", and its core counts are unknown: the two count as the same Configuration until a Maintainer sets them"
			}
		}
	}
	return row
}

// macState is a Mac's state in plain words.
func (s *Server) macState(v *dev, d DeviceView) string {
	switch {
	case d.ActiveJob != "" || d.State == api.StateBusy:
		return "running a job"
	case d.IsVolunteer() && v.windowClosed():
		return "outside its window"
	case d.State == api.StateReady && d.Lease != nil:
		return "leased"
	case d.State == api.StateReady:
		return "free"
	case d.State == api.StateOffline:
		return "not seen"
	}
	return strings.ReplaceAll(string(d.State), "_", " ")
}

// macOr404 is the Mac a button of Macs names.
func (s *Server) macOr404(w http.ResponseWriter, r *http.Request) *dev {
	v := s.dev(r.PathValue("name"))
	if v == nil || v.snapshot().State == api.StateLeft {
		s.notFound(w, r)
		return nil
	}
	return v
}

// backToMacs answers a button of Macs: back to the page under its filter,
// or the page saying why the lab refused.
func (s *Server) backToMacs(w http.ResponseWriter, r *http.Request, err error) {
	show := showOf(r.PostFormValue("show"))
	if err != nil {
		s.renderMacs(w, r, show, err)
		return
	}
	w.Header().Set("Location", s.basePath()+macsPath(show))
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) pMacsLease(w http.ResponseWriter, r *http.Request) {
	v := s.macOr404(w, r)
	if v == nil {
		return
	}
	p := leaser(r)
	var ttl time.Duration
	if raw := r.PostFormValue("hours"); raw != "" {
		hours, err := strconv.Atoi(raw)
		if err != nil || hours < 1 {
			s.backToMacs(w, r, refuse(400, "a Lease's length is a number of hours"))
			return
		}
		ttl = time.Duration(hours) * time.Hour
	}
	// A Lease taken on the site has the person's handle as its label.
	_, err := s.takeLease(v, p, p.Handle, ttl)
	s.backToMacs(w, r, err)
}

func (s *Server) pMacsRenew(w http.ResponseWriter, r *http.Request) {
	v := s.macOr404(w, r)
	if v == nil {
		return
	}
	p := leaser(r)
	v.mu.Lock()
	l := v.liveLeaseLocked()
	v.mu.Unlock()
	if l == nil || !leaseIsOf(l, p) {
		s.backToMacs(w, r, refuse(409, "you hold no Lease on %s to renew", v.snapshot().Name))
		return
	}
	_, err := s.takeLease(v, p, l.Holder, 0)
	s.backToMacs(w, r, err)
}

func (s *Server) pMacsRelease(w http.ResponseWriter, r *http.Request) {
	v := s.macOr404(w, r)
	if v == nil {
		return
	}
	name := v.snapshot().Name
	if !s.sure(w, r, "Release: your Lease on "+name+" ends now", macsPath(showOf(r.PostFormValue("show")))) {
		return
	}
	p := leaser(r)
	if !s.holdsLease(v, p) {
		s.backToMacs(w, r, refuse(409, "you hold no Lease on %s to release", name))
		return
	}
	s.backToMacs(w, r, s.releaseLease(v, p, "", false, ""))
}

// coreCountMax is above any Mac's count; it only keeps nonsense out.
const coreCountMax = 256

func (s *Server) pMacsConfiguration(w http.ResponseWriter, r *http.Request) {
	v := s.macOr404(w, r)
	if v == nil {
		return
	}
	rawCPU, rawGPU := strings.TrimSpace(r.PostFormValue("cpu")), strings.TrimSpace(r.PostFormValue("gpu"))
	var cpu, gpu int
	if rawCPU != "" || rawGPU != "" {
		var errCPU, errGPU error
		cpu, errCPU = strconv.Atoi(rawCPU)
		gpu, errGPU = strconv.Atoi(rawGPU)
		if errCPU != nil || errGPU != nil || cpu < 1 || gpu < 1 || cpu > coreCountMax || gpu > coreCountMax {
			s.backToMacs(w, r, refuse(400, "a Configuration is set with both core counts, each a number from 1; leave both empty to hand it back to the lab"))
			return
		}
	}
	s.setConfiguration(v, leaser(r).Handle, cpu, gpu)
	s.backToMacs(w, r, nil)
}
