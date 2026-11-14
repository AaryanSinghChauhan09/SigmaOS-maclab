package sshsig_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/sshsig"
	"github.com/omacom/omarchy-maclab/internal/sshsig/sshsigtest"
)

const namespace = "omarchy-m-test-release"

// sshKeygenKey makes a key with ssh-keygen itself and returns its private
// key file and its public half as "type base64".
func sshKeygenKey(t *testing.T) (file, public string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen here")
	}
	file = filepath.Join(t.TempDir(), "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "throwaway", "-f", file).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(file + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(pub))
	return file, f[0] + " " + f[1]
}

func TestASignatureMadeBySSHKeygenVerifies(t *testing.T) {
	key, public := sshKeygenKey(t)
	msg := filepath.Join(t.TempDir(), "lab-agent")
	body := bytes.Repeat([]byte("the agent\n"), 5000)
	if err := os.WriteFile(msg, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh-keygen", "-Y", "sign", "-f", key, "-n", namespace, msg).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v: %s", err, out)
	}
	sig, err := os.ReadFile(msg + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := sshsig.Verify(public, namespace, bytes.NewReader(body), sig); err != nil {
		t.Fatalf("a signature ssh-keygen made is refused: %v", err)
	}

	_, other := sshKeygenKey(t)
	changed := append([]byte("x"), body...)
	for name, tc := range map[string]struct {
		key, namespace string
		body, sig      []byte
	}{
		"a changed file":       {public, namespace, changed, sig},
		"another key":          {other, namespace, body, sig},
		"another namespace":    {public, "file", body, sig},
		"no signature":         {public, namespace, body, nil},
		"not a signature":      {public, namespace, body, []byte("-----BEGIN SSH SIGNATURE-----\nAAAA\n-----END SSH SIGNATURE-----\n")},
		"a cut signature":      {public, namespace, body, sig[:len(sig)/2]},
		"a key that is no key": {"ssh-ed25519 AAAA", namespace, body, sig},
		"no key":               {"", namespace, body, sig},
	} {
		if err := sshsig.Verify(tc.key, tc.namespace, bytes.NewReader(tc.body), tc.sig); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

// The tests' own signer makes what ssh-keygen accepts, so a test that signs
// with it tests what a release signed by ssh-keygen would meet.
func TestTheTestSignerMakesWhatSSHKeygenAccepts(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen here")
	}
	k := sshsigtest.NewKey(t)
	dir := t.TempDir()
	body := []byte("a release file\n")
	signers, sig := filepath.Join(dir, "allowed_signers"), filepath.Join(dir, "file.sig")
	if err := os.WriteFile(signers, []byte(`release@omarchy-m-testing.org namespaces="`+namespace+`" `+k.Public+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sig, k.Sign(namespace, body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", signers, "-I", "release@omarchy-m-testing.org", "-n", namespace, "-s", sig)
	cmd.Stdin = bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen refuses the test signer's signature: %v: %s", err, out)
	}
	if err := sshsig.Verify(k.Public, namespace, bytes.NewReader(body), k.Sign(namespace, body)); err != nil {
		t.Fatal(err)
	}
}
