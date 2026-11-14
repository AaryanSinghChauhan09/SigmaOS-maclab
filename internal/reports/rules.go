package reports

import (
	"fmt"
	"math/big"
	"net/http"

	"github.com/omacom/omarchy-maclab/catalogue"
	"github.com/omacom/omarchy-maclab/schema"
)

// SchemaVersion is the report schema these rules accept.
const SchemaVersion = 1

// machineIDKeyBytes is the size of the machine-id key: what the old site
// derived from its SECRET_KEY_BASE (PBKDF2-HMAC-SHA256, salt "report machine
// id from key", 1,000 iterations). The lab keeps the 64 bytes, never the
// secret they came from.
const machineIDKeyBytes = 64

// Rules is what a report is judged against: the report schema, the feature
// catalogue and the machine-id key.
type Rules struct {
	schema       *Schema
	catalogue    *Catalogue
	machineIDKey []byte
}

func NewRules(machineIDKey []byte) (*Rules, error) {
	if len(machineIDKey) != machineIDKeyBytes {
		return nil, fmt.Errorf("reports: the machine-id key is %d bytes, not %d", len(machineIDKey), machineIDKeyBytes)
	}
	reportSchema, err := LoadSchema(schema.ReportV1)
	if err != nil {
		return nil, err
	}
	features, err := LoadCatalogue(catalogue.JSON)
	if err != nil {
		return nil, err
	}
	return &Rules{reportSchema, features, append([]byte(nil), machineIDKey...)}, nil
}

func (r *Rules) Catalogue() *Catalogue { return r.catalogue }

// SchemaErrors gives the problems of a document against the report schema.
func (r *Rules) SchemaErrors(document any) []string { return r.schema.Errors(document) }

// MachineID is the machine id of a Mac's public key.
func (r *Rules) MachineID(key MachineKey) string { return MachineID(r.machineIDKey, key) }

// Refusal is why an upload was refused: the status and the body the old site
// answered with.
type Refusal struct {
	Status  int      `json:"-"`
	Error   string   `json:"error"`
	Details []string `json:"details,omitempty"`
}

// Judged is a report that passed every rule.
type Judged struct {
	// Report is the report less its signature: the key and the signature
	// only prove where it came from, and are not kept.
	Report *Object
	// Body is Report in canonical form, the bytes the Mac signed.
	Body          []byte
	SchemaVersion int
	MachineID     string
}

// Judge applies the old site's upload rules to a request body, in its order:
// JSON, an outdated schema, a missing signature, the schema, the signature,
// check ids the catalogue lacks, the evidence. installURL is where the
// refusals that ask for an update send the user.
func (r *Rules) Judge(body []byte, installURL string) (*Judged, *Refusal) {
	payload, err := Parse(body)
	if err != nil {
		return nil, &Refusal{Status: http.StatusBadRequest, Error: "The report is not valid JSON."}
	}
	refuse := func(message string, details ...string) (*Judged, *Refusal) {
		return nil, &Refusal{http.StatusUnprocessableEntity, message, details}
	}
	upgrade := "Update omarchy-m-test (curl -fsSL " + installURL + " | bash) and run it again."
	accepts := fmt.Sprint(SchemaVersion)

	report, _ := payload.(*Object)
	if report != nil {
		if version, ok := report.members["schema_version"].(*big.Int); ok && version.Cmp(big.NewInt(SchemaVersion)) < 0 {
			return refuse("This report uses report schema v"+version.String()+", which is outdated: the site accepts v"+accepts+". "+upgrade,
				"schema_version "+version.String()+" is older than "+accepts)
		}
		if _, signed := report.members["signature"]; !signed {
			return refuse("This report isn't signed with its Mac's key, which the site requires. "+
				"omarchy-m-test signs every report by itself (it needs ssh-keygen, from openssh). "+upgrade, "signature is missing")
		}
	}
	if problems := r.schema.Errors(payload); len(problems) > 0 {
		return refuse("The report does not match report schema v"+accepts+".", problems...)
	}
	key, err := VerifySignature(report, NamespaceReport)
	if err != nil {
		return refuse("The report's signature is not valid, so it was refused.", "signature: "+err.Error())
	}
	if unknown := r.catalogue.UnknownCheckIDs(report); len(unknown) > 0 {
		for i, id := range unknown {
			unknown[i] = "unknown check id " + id
		}
		return refuse(fmt.Sprintf("The report has checks this site doesn't know (feature catalogue v%d). %s", r.catalogue.Version(), upgrade), unknown...)
	}
	if problems := EvidenceErrors(report); len(problems) > 0 {
		return refuse("The report's evidence must be text and at most 64 KiB.", problems...)
	}
	kept := report.without("signature")
	text, err := Canonical(kept)
	if err != nil {
		return refuse("The report's signature is not valid, so it was refused.", "signature: "+err.Error())
	}
	return &Judged{Report: kept, Body: text, SchemaVersion: SchemaVersion, MachineID: r.MachineID(key)}, nil
}
