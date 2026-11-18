package server

// Mac lab controllers: any macOS Mac with the lab-controller install is an
// Out-of-band controller for the Macs cabled to it. labd reaches each over
// HTTPS, its certificate pinned, with its own token; the mini's own
// controller is "mini", on loopback, as it always was. The list is a file in
// labd's data and each token a file of labd's alone beside it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/oob"
)

type savedController struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Fingerprint string    `json:"fingerprint"`
	Index       int       `json:"index"`
	RIDs        []int     `json:"rids"`
	AddedBy     string    `json:"added_by"`
	At          time.Time `json:"at"`
}

var controllerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (s *Server) controllersFile() string { return filepath.Join(s.cfg.DataDir, "controllers.json") }

func (s *Server) controllerToken(name string) string {
	return filepath.Join(s.cfg.DataDir, "controllers", name+".token")
}

func (s *Server) savedControllers() ([]savedController, error) {
	b, err := os.ReadFile(s.controllersFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []savedController
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", s.controllersFile(), err)
	}
	return out, nil
}

func (s *Server) writeControllers(list []savedController) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.controllersFile() + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.controllersFile())
}

// loadControllers puts the saved Mac lab controllers in the fleet.
func (s *Server) loadControllers() error {
	list, err := s.savedControllers()
	if err != nil {
		return fmt.Errorf("reading the controllers: %w", err)
	}
	for _, sc := range list {
		c, err := oob.NewRemoteClient(sc.URL, s.controllerToken(sc.Name), sc.Fingerprint)
		if err != nil {
			return fmt.Errorf("the controller %s: %w", sc.Name, err)
		}
		s.oob.Put(oob.Member{Name: sc.Name, Index: sc.Index, RIDs: sc.RIDs, Client: c})
	}
	return nil
}

func (s *Server) hControllers(w http.ResponseWriter, r *http.Request) {
	saved, err := s.savedControllers()
	if err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	by := map[string]savedController{}
	for _, sc := range saved {
		by[sc.Name] = sc
	}
	out := []api.Controller{}
	for _, m := range s.oob.Members() {
		c := api.Controller{Name: m.Name, Ports: []string{}}
		for _, p := range m.Ports() {
			c.Ports = append(c.Ports, s.oob.PortName(p))
		}
		if sc, ok := by[m.Name]; ok {
			c.URL, c.Fingerprint, c.AddedBy = sc.URL, m.Client.Pinned(), sc.AddedBy
		}
		out = append(out, c)
	}
	writeJSON(w, out)
}

func (s *Server) hAddController(w http.ResponseWriter, r *http.Request) {
	var in api.ControllerAdd
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		httpErr(w, 400, "%v", err)
		return
	}
	sc, err := s.addController(r.Context(), in, leaser(r).Handle)
	if err != nil {
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	s.log.Printf("controller %s added by %s: %s", sc.Name, sc.AddedBy, sc.URL)
	writeJSON(w, map[string]any{"name": sc.Name, "rids": sc.RIDs})
}

func (s *Server) addController(ctx context.Context, in api.ControllerAdd, by string) (savedController, error) {
	s.controllersMu.Lock()
	defer s.controllersMu.Unlock()
	switch {
	case !controllerName.MatchString(in.Name):
		return savedController{}, refuse(400, "%q is no controller name: lower case letters, digits and -, up to 32", in.Name)
	case in.Name == oob.MiniController:
		return savedController{}, refuse(409, "%q is the lab server's own controller", in.Name)
	case len(in.Token) < 32:
		return savedController{}, refuse(400, "the controller's token is 32 characters or more, as its install makes it")
	}
	saved, err := s.savedControllers()
	if err != nil {
		return savedController{}, err
	}
	index := 1
	for _, sc := range saved {
		if sc.Name == in.Name {
			return savedController{}, refuse(409, "there is a controller %s already: remove it first", in.Name)
		}
		index = max(index, sc.Index+1)
	}
	tokenFile := s.controllerToken(in.Name)
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
		return savedController{}, err
	}
	c, err := oob.NewRemoteClient(in.URL, tokenFile, in.Fingerprint)
	if err != nil {
		return savedController{}, refuse(400, "%v", err)
	}
	if err := os.WriteFile(tokenFile, []byte(in.Token+"\n"), 0o600); err != nil {
		return savedController{}, err
	}
	actx, cancel := context.WithTimeout(ctx, 20*time.Second)
	rids, err := c.RIDs(actx)
	cancel()
	if err != nil {
		os.Remove(tokenFile)
		return savedController{}, refuse(http.StatusBadGateway, "the controller %s was not added: %v", in.Name, err)
	}
	sc := savedController{Name: in.Name, URL: c.URL, Fingerprint: c.Pinned(), Index: index, RIDs: rids, AddedBy: by, At: s.now()}
	if err := s.writeControllers(append(saved, sc)); err != nil {
		os.Remove(tokenFile)
		return savedController{}, err
	}
	s.oob.Put(oob.Member{Name: sc.Name, Index: sc.Index, RIDs: rids, Client: c})
	return sc, nil
}

func (s *Server) hRemoveController(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.removeController(name); err != nil {
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	s.log.Printf("controller %s removed by %s", name, leaser(r).Handle)
	writeJSON(w, map[string]string{"removed": name})
}

// removeController drops a Mac lab controller and every Sighting on its
// ports: the lab can no longer reset a Mac through it.
func (s *Server) removeController(name string) error {
	s.controllersMu.Lock()
	defer s.controllersMu.Unlock()
	if name == oob.MiniController {
		return refuse(409, "the mini's own controller is not removed: labd is started without --oobd-token-file for none")
	}
	saved, err := s.savedControllers()
	if err != nil {
		return err
	}
	var keep []savedController
	var gone *savedController
	for i := range saved {
		if saved[i].Name == name {
			gone = &saved[i]
			continue
		}
		keep = append(keep, saved[i])
	}
	if gone == nil {
		return refuse(404, "no controller %s", name)
	}
	if err := s.writeControllers(keep); err != nil {
		return err
	}
	var ports []int
	for _, m := range s.oob.Members() {
		if m.Name == name {
			ports = m.Ports()
		}
	}
	s.oob.Remove(name)
	os.Remove(s.controllerToken(name))
	s.cables.looking.Lock()
	s.cables.mu.Lock()
	for mac, sg := range s.cables.held {
		for _, p := range ports {
			if sg.rid == p {
				s.cables.dropLocked(mac, "its controller "+name+" was removed", s.now())
			}
		}
	}
	for _, p := range ports {
		delete(s.cables.ports, p)
	}
	s.cables.mu.Unlock()
	s.cables.looking.Unlock()
	s.saveSightings()
	return nil
}
