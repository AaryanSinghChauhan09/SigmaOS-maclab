package server

import (
	"io"
	"os"
	"path/filepath"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/detect"
)

// A Mac's Configuration is its board with its CPU and GPU core counts, and
// labd works it out: the board from the Mac's check-in; the counts from the
// repository's table for a board sold with one Configuration, otherwise
// from the kernel log of the Mac's latest job on its Known-good kernel. A
// Maintainer can set or correct the counts, and that stands over both.
// lab-agent reports no core count and is not asked to.

// kernelLogRead is how much of a kernel log labd reads for the two lines:
// both are printed in the first seconds of a boot.
const kernelLogRead = 4 << 20

// configuration is v's Configuration as labd works it out now.
func (s *Server) configuration(v *dev) api.Configuration {
	v.mu.Lock()
	c, look := v.configurationLocked()
	v.mu.Unlock()
	if !look {
		return c
	}
	cpu, gpu := s.coreCountsFromJobs(v)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.coresLooked = true
	if kept := v.d.Configuration; cpu+gpu > 0 && (kept == nil || kept.SetBy == "" && (kept.CPU != cpu || kept.GPU != gpu)) {
		v.d.Configuration = &api.Configuration{CPU: cpu, GPU: gpu}
		v.saveLocked()
	}
	c, _ = v.configurationLocked()
	return c
}

// configurationLocked is the Configuration from what labd holds, and
// whether the Mac's jobs should be looked through for its core counts
// first: the table has no row for the board, no Maintainer set them, and
// nothing has looked since the Mac's last job. Caller holds v.mu.
func (v *dev) configurationLocked() (c api.Configuration, look bool) {
	c.Board = detect.Board(v.d.Facts.Compatible)
	kept := v.d.Configuration
	if kept != nil && kept.SetBy != "" {
		c.CPU, c.GPU, c.SetBy = kept.CPU, kept.GPU, kept.SetBy
		return c, false
	}
	if cpu, gpu, ok := detect.OneConfiguration(c.Board); ok {
		c.CPU, c.GPU = cpu, gpu
		return c, false
	}
	if kept != nil {
		c.CPU, c.GPU = kept.CPU, kept.GPU
	}
	return c, !v.coresLooked && v.d.KnownGood != ""
}

// coreCountsFromJobs reads the core counts from the kernel log of v's
// latest job that booted its Known-good kernel, in this enrollment.
func (s *Server) coreCountsFromJobs(v *dev) (cpu, gpu int) {
	d := v.snapshot()
	jobs, err := s.store.jobs(d.Name, 200, false)
	if err != nil {
		s.log.Printf("%s: its Configuration: %v", d.Name, err)
		return 0, 0
	}
	for _, j := range jobs {
		if j.DeviceID != d.ID || !j.Result.Booted || j.Result.BootKernel != d.KnownGood {
			continue
		}
		f, err := os.Open(filepath.Join(s.jobDir(j.ID), "boot0", "kernel.txt"))
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, kernelLogRead))
		f.Close()
		if err != nil {
			continue
		}
		return detect.CoreCounts(string(b))
	}
	return 0, 0
}

// forgetCoreCounts has labd look through v's jobs again the next time its
// Configuration is asked for: a job ended, or its Known-good kernel changed.
func (s *Server) forgetCoreCounts(v *dev) {
	v.mu.Lock()
	v.coresLooked = false
	v.mu.Unlock()
}

// setConfiguration is a Maintainer setting or correcting v's core counts.
// Both 0 hands the Configuration back to labd.
func (s *Server) setConfiguration(v *dev, by string, cpu, gpu int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cpu == 0 && gpu == 0 {
		v.d.Configuration, v.coresLooked = nil, false
		s.log.Printf("%s: @%s handed its Configuration back to the lab", v.d.Name, by)
	} else {
		v.d.Configuration = &api.Configuration{CPU: cpu, GPU: gpu, SetBy: by}
		s.log.Printf("%s: @%s set its Configuration to %d CPU and %d GPU cores", v.d.Name, by, cpu, gpu)
	}
	v.saveLocked()
	v.publishLocked()
}
