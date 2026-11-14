package server

// The lab's own software on a Mac.
//
// lab-agent comes from the lab server only: labd hands out the one built
// with it, with its signature beside it, to the Join command's token and to
// an enrolled Mac's own device secret. The Mac checks it: the Join command
// against the release key written into the line, a running lab-agent against
// the key it pins (`sudo lab-agent update`), refusing an older one.
//
// Nothing pushes an agent to a Volunteer's Mac. Its Volunteer is told once,
// by a Notice, and My Macs gives the one line to run at the Mac; until then
// the "needs lab-agent X or later" refusals apply. A Lab-owned Mac's agent
// is updated by a Maintainer, through its running agent, while it is idle
// (`lab agent-update`). There is no fleet-wide minimum agent.

import (
	"net/http"
	"os"
	"time"

	"github.com/omacom/omarchy-maclab/installer"
	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
)

// agentUpdateMinAgent is the first lab-agent that updates itself.
const agentUpdateMinAgent = "0.10.0"

// agentUpdateWait is how long labd waits for a Mac to fetch, check and
// install the lab's lab-agent.
const agentUpdateWait = 45 * time.Second

const (
	// volunteerUpdateLine is what a Volunteer runs at their Mac.
	volunteerUpdateLine = "sudo lab-agent update"
	// updateNotice is the fixed wording of the Notice about it.
	updateNotice = "An update is waiting for this Mac. Open My Macs."
	noticeTitle  = "Omarchy Mac Lab"
)

// labAgent is the number of the lab-agent this labd hands out: the one it
// was built with.
func (s *Server) labAgent() string {
	if s.cfg.LabAgent != "" {
		return s.cfg.LabAgent
	}
	return agent.Version
}

// agentBehind is the lab's lab-agent number when d's is older, else "".
func (s *Server) agentBehind(d api.Device) string {
	if lab := s.labAgent(); d.Facts.AgentVersion != "" && !versionAtLeast(d.Facts.AgentVersion, lab) {
		return lab
	}
	return ""
}

// agentUpdateLine is the one line My Macs gives the Volunteer of a Mac whose
// lab-agent is older than the lab's; "" for any other Mac.
func agentUpdateLine(d DeviceView) string {
	if d.LabAgent != "" && d.IsVolunteer() {
		return volunteerUpdateLine
	}
	return ""
}

// noticeUpdateLocked tells the Volunteer of a Mac whose lab-agent is older
// than the lab's, once for each lab-agent of the lab. It reports whether the
// Mac's record changed. Caller holds v.mu.
func (v *dev) noticeUpdateLocked() bool {
	lab := v.s.agentBehind(v.d)
	if lab == "" || !v.d.IsVolunteer() || v.d.UpdateNoticed == lab {
		return false
	}
	v.d.UpdateNoticed = lab
	v.s.notify(Notification{Device: v.d.Name, Volunteer: true, MacTopic: v.s.macTopicURL(v.d.Enrollment), Priority: "default",
		Title: noticeTitle, Body: v.d.Name + ": " + updateNotice})
	return true
}

// agentSig is the signature beside the lab-agent labd hands out, or false.
func (s *Server) agentSig() ([]byte, bool) {
	if s.cfg.AgentBinary == "" {
		return nil, false
	}
	b, err := os.ReadFile(s.cfg.AgentBinary + ".sig")
	return b, err == nil && len(b) > 0
}

// hAgentSig hands out the signature of the lab-agent built with this labd.
func (s *Server) hAgentSig(w http.ResponseWriter, r *http.Request) {
	sig, ok := s.agentSig()
	if !ok {
		httpErr(w, 404, "this lab's lab-agent has no signature: it was not built as part of a Lab release")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(sig)
}

// releaseKey is the release key this labd's files are checked against: the
// one it was built with, else the repository's.
func (s *Server) releaseKey() string {
	if s.cfg.ReleaseKey != "" {
		return s.cfg.ReleaseKey
	}
	if m := rePinnedKey.FindSubmatch(installer.Script); m != nil {
		return string(m[2])
	}
	return ""
}

// hAgentUpdate is `lab agent-update <mac>`: a Maintainer has a Lab-owned Mac
// that is idle take the lab's lab-agent.
func (s *Server) hAgentUpdate(w http.ResponseWriter, r *http.Request) {
	v := s.devOr404(w, r)
	if v == nil {
		return
	}
	v.mu.Lock()
	d, alive, lease, waiting := v.d, v.aliveLocked(), v.liveLeaseLocked(), len(v.queue)+len(v.parked)
	idle := d.State == api.StateNew || d.State == api.StateReady
	v.mu.Unlock()
	_, signed := s.agentSig()
	switch {
	case d.IsVolunteer():
		httpErr(w, 409, "%s is a Volunteer's Mac, and lab-agent is never pushed to a Volunteer's Mac: its Volunteer runs `%s` at the Mac, and My Macs shows them the line", d.Name, volunteerUpdateLine)
	case !signed:
		httpErr(w, 409, "this lab has no signed lab-agent to hand out: it was not Deployed from a Lab release")
	case d.ActiveJob != "":
		httpErr(w, 409, "%s is not idle: job %s runs on it. lab agent-update waits for no job and no Lease", d.Name, d.ActiveJob)
	case lease != nil:
		httpErr(w, 409, "%s is not idle: %s holds a Lease on it until %s. lab agent-update waits for no job and no Lease", d.Name, lease.Who(), lease.Expires.Format(time.TimeOnly))
	case waiting > 0:
		httpErr(w, 409, "%s is not idle: a job waits for it. lab agent-update waits for no job and no Lease", d.Name)
	case !alive:
		httpErr(w, 409, "%s is offline, so its lab-agent cannot be updated now", d.Name)
	case !idle:
		httpErr(w, 409, "%s is not idle: it is %s. lab agent-update waits for no job and no Lease", d.Name, d.State)
	case !versionAtLeast(d.Facts.AgentVersion, agentUpdateMinAgent):
		httpErr(w, 409, "%s runs lab-agent %s; lab agent-update needs %s or later, the first that updates itself: Join the Mac again to give it the lab's", d.Name, orStr(d.Facts.AgentVersion, "(unknown)"), agentUpdateMinAgent)
	default:
		var res api.AgentUpdate
		if err := v.call(r.Context(), api.CmdAgentUpdate, "", nil, agentUpdateWait, &res); err != nil {
			httpErr(w, 502, "%s did not update its lab-agent: %v", d.Name, err)
			return
		}
		if res.Changed {
			s.log.Printf("%s: lab-agent %s -> %s by %s", d.Name, res.From, res.To, callerOf(r).maintainer)
		}
		writeJSON(w, res)
	}
}
