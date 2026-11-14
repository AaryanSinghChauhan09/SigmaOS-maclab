package agent

// Stamped in by the Makefile. LabRelease is the tag of the Lab release this
// agent came from; the agent's own number stays Version, which labd compares.
// ReleaseKey is the release key as "type base64": the pin a new agent's
// signature is checked against.
var (
	LabRelease = "dev"
	ReleaseKey = ""
)

// How every file of a Lab release is signed (release/lib.sh).
const (
	ReleaseSigner    = "release@omarchy-m-testing.org"
	ReleaseNamespace = "omarchy-m-test-release"
)

// ReleaseSigners is the pinned release key as an allowed_signers file for
// ssh-keygen -Y verify, or "" in a build that carries none.
func ReleaseSigners() string {
	if ReleaseKey == "" {
		return ""
	}
	return ReleaseSigner + ` namespaces="` + ReleaseNamespace + `" ` + ReleaseKey + "\n"
}
