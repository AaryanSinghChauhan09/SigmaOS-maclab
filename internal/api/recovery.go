package api

import "time"

const (
	RecoveryNetworkOnly = "network-only"
	// RecoverySerialAssisted is a mode of the old lab, kept as a name so it
	// can be refused: see NoSerialConsole.
	RecoverySerialAssisted = "serial-assisted"
)

// NoSerialConsole is what the lab answers wherever the serial console is
// asked for: a Hard reset is its only out-of-band function.
const NoSerialConsole = "The serial console is not part of Omarchy Mac Lab."

func ValidRecoveryMode(mode string) bool {
	return mode == "" || mode == RecoveryNetworkOnly || mode == RecoverySerialAssisted
}

type RecoveryCapabilities struct {
	Schema               int      `json:"schema"`
	Modes                []string `json:"modes"`
	PerJobOOBIsolation   bool     `json:"per_job_oob_isolation"`
	TransactionIsolation bool     `json:"transaction_isolation"`
}

type RecoveryModeRequest struct {
	Mode        string `json:"mode"`
	Holder      string `json:"holder"`
	Transaction string `json:"transaction"`
}
type RecoveryModeSelection struct {
	Schema      int    `json:"schema"`
	Device      string `json:"device"`
	Mode        string `json:"mode"`
	Holder      string `json:"holder"`
	Transaction string `json:"transaction"`
	Quiescent   bool   `json:"quiescent"`
}

// What the lab knows of the cable that joins a Lab-owned Mac to the lab
// server.
const (
	// CableSighted: the lab knows which of the server's cables the Mac is on.
	CableSighted = "sighted"
	// CableNotSighted: a cable with nothing sighted on it could be the Mac's.
	CableNotSighted = "not sighted"
	// CableNone: no cable is left that could be the Mac's.
	CableNone = "no cable"
)

// Cable is a Lab-owned Mac's place on the lab server's cables, and with it
// whether a Hard reset can reach the Mac. A Volunteer's Mac has none.
type Cable struct {
	State string `json:"state"`
	// Port is the lab server's port the Mac is sighted on: left, middle or
	// right, facing the back.
	Port string `json:"port,omitempty"`
	// Proved: the Mac was placed by a Proving reset, not by its name.
	Proved bool      `json:"proved,omitempty"`
	Since  time.Time `json:"since,omitzero"`
	// NoHardReset is why no Hard reset can reach the Mac now; "" when one can.
	NoHardReset string `json:"no_hard_reset,omitempty"`
}

// String is the Cable in a few words: sighted on the right port.
func (c *Cable) String() string {
	if c == nil {
		return "none: a Volunteer's Mac"
	}
	if c.State != CableSighted {
		return c.State
	}
	s := "sighted on the " + c.Port + " port"
	if c.Proved {
		s += " by a Proving reset"
	}
	return s
}

// OOBPort is what the Out-of-band controller shows on one of the lab
// server's ports.
type OOBPort struct {
	Port      string `json:"port"`
	Connected bool   `json:"connected"`
	// Name is the name the Mac on it shows; empty when it shows none.
	Name      string `json:"name,omitempty"`
	ProductID int    `json:"product_id,omitempty"`
	PlugCount int    `json:"plug_count"`
	// Mac is the Lab-owned Mac sighted on it, "" for none.
	Mac string `json:"mac,omitempty"`
}

// OOBStatus is what `lab oob <mac>` shows.
type OOBStatus struct {
	Device string `json:"device"`
	// Cable is nil for a Volunteer's Mac.
	Cable *Cable `json:"cable,omitempty"`
	// Controller is "healthy", or why the Out-of-band controller is not.
	Controller string `json:"controller,omitempty"`
	// Ports are the lab server's ports as the controller last showed them.
	Ports []OOBPort `json:"ports,omitempty"`
}

// ControllerHealthy is OOBStatus.Controller for a controller that answers.
const ControllerHealthy = "healthy"

// Proving is the answer to a Proving reset that counted.
type Proving struct {
	Device string `json:"device"`
	Port   string `json:"port"`
	Cable  Cable  `json:"cable"`
}
