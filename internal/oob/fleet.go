package oob

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// MiniController is the name of the controller on the lab server itself.
const MiniController = "mini"

// perController spaces the controllers' ports in the lab's one numbering:
// a controller's port is its index times this, plus its RID. The mini's
// own is index 0, so its ports keep the RIDs they always had.
const perController = 100

// Member is one controller of the Fleet.
type Member struct {
	Name   string
	Index  int
	RIDs   []int
	Client *Client
}

// Fleet is every Out-of-band controller labd has: the mini's own, and each
// Mac lab controller added since. It numbers all their ports as one, so a
// Sighting names a port of the lab and a reset goes to the controller that
// has it.
type Fleet struct {
	mu      sync.Mutex
	members []Member
}

// NewFleet starts with the mini's own controller, or with none for nil.
func NewFleet(mini *Client) *Fleet {
	f := &Fleet{}
	if mini != nil {
		f.members = []Member{{Name: MiniController, RIDs: slices.Clone(RIDs), Client: mini}}
	}
	return f
}

// Put adds a controller, or replaces the one of that name.
func (f *Fleet) Put(m Member) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = slices.DeleteFunc(f.members, func(o Member) bool { return o.Name == m.Name })
	f.members = append(f.members, m)
	slices.SortFunc(f.members, func(a, b Member) int { return a.Index - b.Index })
}

// Remove drops the controller of that name; false when there was none.
func (f *Fleet) Remove(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.members)
	f.members = slices.DeleteFunc(f.members, func(o Member) bool { return o.Name == name })
	return len(f.members) != n
}

// Members are the controllers, the mini's first.
func (f *Fleet) Members() []Member {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.members)
}

// Empty is a lab with no controller at all.
func (f *Fleet) Empty() bool { return len(f.Members()) == 0 }

// Mini is the mini's own controller, nil for none.
func (f *Fleet) Mini() *Client {
	for _, m := range f.Members() {
		if m.Index == 0 {
			return m.Client
		}
	}
	return nil
}

// Ports are the lab's numbers of m's ports.
func (m Member) Ports() []int {
	out := make([]int, len(m.RIDs))
	for i, r := range m.RIDs {
		out[i] = m.Index*perController + r
	}
	return out
}

// Ports are the lab's numbers of every port of every controller.
func (f *Fleet) Ports() []int {
	var out []int
	for _, m := range f.Members() {
		out = append(out, m.Ports()...)
	}
	return out
}

// Of is the controller with the lab's port number port, and its own RID.
func (f *Fleet) Of(port int) (Member, int, bool) {
	for _, m := range f.Members() {
		if port/perController == m.Index && slices.Contains(m.RIDs, port%perController) {
			return m, port % perController, true
		}
	}
	return Member{}, 0, false
}

// PortName is how people name a port of the lab: left, middle or right on
// the mini, NAME:RID on another controller.
func (f *Fleet) PortName(port int) string {
	if port < perController {
		return PortName(port)
	}
	if m, rid, ok := f.Of(port); ok {
		return m.Name + ":" + strconv.Itoa(rid)
	}
	return fmt.Sprintf("port %d of a controller the lab no longer has", port)
}

// PortOf is the lab's number of the port people call name.
func (f *Fleet) PortOf(name string) (int, bool) {
	ctl, r, found := strings.Cut(name, ":")
	if !found {
		if rid, ok := RIDOf(name); ok {
			if _, _, ok := f.Of(rid); ok {
				return rid, true
			}
		}
		return 0, false
	}
	rid, err := strconv.Atoi(r)
	if err != nil {
		return 0, false
	}
	for _, m := range f.Members() {
		if m.Name == ctl && slices.Contains(m.RIDs, rid) {
			return m.Index*perController + rid, true
		}
	}
	return 0, false
}

// Names are the names of every port of the lab, for a refusal.
func (f *Fleet) Names() []string {
	var out []string
	for _, p := range f.Ports() {
		out = append(out, f.PortName(p))
	}
	return out
}

func (f *Fleet) route(port int) (*Client, int, error) {
	m, rid, ok := f.Of(port)
	if !ok {
		return nil, 0, fmt.Errorf("the lab has no controller with port %d", port)
	}
	return m.Client, rid, nil
}

func renumber(p Port, port int, err error) (Port, error) {
	if err == nil {
		p.RID = port
	}
	return p, err
}

// Port is Client.Port on the controller that has port.
func (f *Fleet) Port(ctx context.Context, port int) (Port, error) {
	c, rid, err := f.route(port)
	if err != nil {
		return Port{}, err
	}
	p, err := c.Port(ctx, rid)
	return renumber(p, port, err)
}

// Probe is Client.Probe on the controller that has port.
func (f *Fleet) Probe(ctx context.Context, port int) (Port, error) {
	c, rid, err := f.route(port)
	if err != nil {
		return Port{}, err
	}
	p, err := c.Probe(ctx, rid)
	return renumber(p, port, err)
}

// Reset is Client.Reset on the controller that has port.
func (f *Fleet) Reset(ctx context.Context, port int, seen *Seen) error {
	c, rid, err := f.route(port)
	if err != nil {
		return err
	}
	return c.Reset(ctx, rid, seen)
}
