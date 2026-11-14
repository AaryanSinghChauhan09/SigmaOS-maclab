package server

// A Deploy and a Maintenance run restart labd, and no job outlives labd. So
// each first drains: labd starts no further job, and the script waits until
// those that run have ended. A job that waits stays queued, in the store,
// and starts after the restart; should the script give up instead, it lifts
// the drain and the job starts then. The drain is not stored: a labd that
// starts is never draining.
//
// A running job may wait for a build, so builds are handed out while any
// job runs. Once none does the Builder gets no further build, and the drain
// is over when the builds that run have ended as well.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

type drain struct {
	mu sync.Mutex
	// lifted is nil while labd is not draining; closed when a drain is lifted.
	lifted chan struct{}
	// jobs counts the jobs inside runJob.
	jobs int
}

// startJob is called before a queued job is run. While jobs are drained it
// waits; it reports false when the device's loop should end instead.
func (s *Server) startJob(v *dev) bool {
	for {
		s.drain.mu.Lock()
		lifted := s.drain.lifted
		if lifted == nil {
			s.drain.jobs++
			s.drain.mu.Unlock()
			return true
		}
		s.drain.mu.Unlock()
		select {
		case <-lifted:
		case <-s.ctx.Done():
			return false
		case <-v.stop:
			return false
		}
	}
}

func (s *Server) endJob() {
	s.drain.mu.Lock()
	s.drain.jobs--
	s.drain.mu.Unlock()
}

// buildsHeld reports whether the Builder gets no build now: jobs are
// drained and none runs that could wait for one.
func (s *Server) buildsHeld() bool {
	s.drain.mu.Lock()
	defer s.drain.mu.Unlock()
	return s.drain.lifted != nil && s.drain.jobs == 0
}

// drainJobs starts a drain, or goes on with the one there is, and says how
// many jobs and builds still run.
func (s *Server) drainJobs() (jobs, builds int, err error) {
	s.builds.state.Lock()
	defer s.builds.state.Unlock()
	running, err := s.store.builds(`state='` + string(api.BuildRunning) + `'`)
	if err != nil {
		return 0, 0, err
	}
	s.drain.mu.Lock()
	defer s.drain.mu.Unlock()
	if s.drain.lifted == nil {
		s.drain.lifted = make(chan struct{})
		s.log.Printf("jobs are drained: %d jobs and %d builds still run", s.drain.jobs, len(running))
	}
	return s.drain.jobs, len(running), nil
}

func (s *Server) liftDrain() {
	s.drain.mu.Lock()
	defer s.drain.mu.Unlock()
	if s.drain.lifted != nil {
		close(s.drain.lifted)
		s.drain.lifted = nil
		s.log.Printf("the drain of jobs is lifted")
	}
}

// partsNow is what a Deploy asks once labd is back: whether the Builder
// asks this labd for work and whether the Out-of-band controller answers
// it. "none" is a part this lab does not have yet.
func (s *Server) partsNow(ctx context.Context) (builder, controller string) {
	builder, controller = partNone, partNone
	if named, err := s.store.builders(); err == nil && len(named) > 0 {
		builder = partOK
		for _, b := range named {
			if alive, _ := s.builderAlive(b.Name); !alive {
				builder = "silent"
			}
		}
	}
	if c := s.cfg.OOB; c != nil {
		if _, err := os.Lstat(c.TokenFile); err == nil {
			actx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			controller = partOK
			// What a port shows, which runs no tool.
			if _, err := c.Port(actx, 0); err != nil {
				controller = partBad
			}
		}
	}
	return builder, controller
}

func (s *Server) opsDrain(w http.ResponseWriter, r *http.Request) {
	jobs, builds, err := s.drainJobs()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "the builds could not be read")
		return
	}
	fmt.Fprintf(w, "%d %d\n", jobs, builds)
}

func (s *Server) opsLift(w http.ResponseWriter, r *http.Request) {
	s.liftDrain()
	fmt.Fprint(w, "lifted\n")
}

func (s *Server) opsParts(w http.ResponseWriter, r *http.Request) {
	builder, controller := s.partsNow(r.Context())
	fmt.Fprintf(w, "builder %s\ncontroller %s\n", builder, controller)
}
