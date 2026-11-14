package cloudtest

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tunnelScript = "tunnel.sh"
	tunnelName   = "omarchy-mac-lab"
)

// credential reads what tunnel.sh wrote: cloudflared's token form, one line
// of base64 around {"a": account, "t": tunnel, "s": secret}.
func credential(t *testing.T, file string) (account, tunnel, secret string) {
	t.Helper()
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the credential is %v, want only its owner to read it", info.Mode().Perm())
	}
	b, _ := os.ReadFile(file)
	if strings.Count(strings.TrimSpace(string(b)), "\n") != 0 {
		t.Fatalf("the credential is not one line")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("the credential is not base64: %v", err)
	}
	var c struct{ A, T, S string }
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("the credential holds no JSON: %v", err)
	}
	return c.A, c.T, c.S
}

func TestTunnelCheckSaysThereIsNoTunnel(t *testing.T) {
	f := asSpec(t)
	out, code := f.run(tunnelScript, "--check")
	if code != 1 || !strings.Contains(out, "drifted  the tunnel "+tunnelName) {
		t.Errorf("exit %d\n%s", code, out)
	}
	if w := writes(f.sent()); len(w) != 0 {
		t.Errorf("a check wrote:\n%s", describe(w))
	}
}

func TestTunnelApplyNeedsSomewhereForTheCredential(t *testing.T) {
	f := asSpec(t)
	out, code := f.run(tunnelScript, "--apply")
	if code == 0 || !strings.Contains(out, "--credential-to") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if w := writes(f.sent()); len(w) != 0 {
		t.Errorf("a tunnel was made with nowhere to put its credential:\n%s", describe(w))
	}

	// A file that is already there is never written over.
	dest := filepath.Join(t.TempDir(), "credential")
	os.WriteFile(dest, []byte("an earlier one\n"), 0o600)
	out, code = f.run(tunnelScript, "--apply", "--credential-to", dest)
	if code == 0 || len(writes(f.sent())) != 0 {
		t.Errorf("exit %d; want a refusal before any write\n%s", code, out)
	}
	if b, _ := os.ReadFile(dest); string(b) != "an earlier one\n" {
		t.Error("the earlier file was written over")
	}
}

func TestTunnelApplyMakesTheTunnelItsCredentialAndTheRecord(t *testing.T) {
	f := asSpec(t)
	dest := filepath.Join(t.TempDir(), "credential")

	out, code := f.run(tunnelScript, "--apply", "--credential-to", dest)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(f.tunnels) != 1 {
		t.Fatalf("%d tunnels, want the one", len(f.tunnels))
	}
	tn := f.tunnels[0]
	if tn["name"] != tunnelName || tn["config_src"] != "local" {
		t.Errorf("the tunnel: %v", tn)
	}
	sent, _ := tn["tunnel_secret"].(string)
	if raw, err := base64.StdEncoding.DecodeString(sent); err != nil || len(raw) < 32 {
		t.Errorf("the tunnel's secret is not 32 bytes or more of base64 (%d bytes, %v)", len(raw), err)
	}
	account, tunnel, secret := credential(t, dest)
	if account != accountID || tunnel != tn["id"] || secret != sent {
		t.Errorf("the credential is not this tunnel's: account %q, tunnel %q", account, tunnel)
	}
	b, _ := os.ReadFile(dest)
	for _, never := range []string{sent, strings.TrimSpace(string(b))} {
		if strings.Contains(out, never) {
			t.Errorf("the output holds the tunnel's secret or its credential:\n%s", out)
		}
	}
	if len(f.records) != 1 {
		t.Fatalf("%d records, want the Lab address's", len(f.records))
	}
	rec := f.records[0]
	if rec["type"] != "CNAME" || rec["name"] != labHost || rec["content"] != tunnel+".cfargotunnel.com" || rec["proxied"] != true {
		t.Errorf("the record: %v", rec)
	}
	f.sent()

	// Again: nothing is made twice, and the credential is left alone.
	out, code = f.run(tunnelScript, "--apply", "--credential-to", filepath.Join(t.TempDir(), "another"))
	if w := writes(f.sent()); code != 0 || len(w) != 0 {
		t.Errorf("a second --apply: exit %d, wrote:\n%s%s", code, describe(w), out)
	}

	// A check wants cloudflared connected.
	out, code = f.run(tunnelScript, "--check")
	if code != 1 || !strings.Contains(out, "not connected") {
		t.Errorf("a check of a tunnel nothing runs: exit %d\n%s", code, out)
	}
	f.mu.Lock()
	f.tunnels[0]["status"] = "healthy"
	f.mu.Unlock()
	if out, code := f.run(tunnelScript, "--check"); code != 0 {
		t.Errorf("a check of a connected tunnel: exit %d\n%s", code, out)
	}
}

func TestTunnelRecordThatPointsElsewhereIsNamedAndPutRight(t *testing.T) {
	f := asSpec(t)
	f.tunnels = []map[string]any{{"id": "11111111-2222-4333-8444-555555555555", "name": tunnelName, "config_src": "local", "status": "healthy"}}
	f.records = []map[string]any{{"id": "rec0", "type": "CNAME", "name": labHost, "content": "elsewhere.example.org", "proxied": false}}

	out, code := f.run(tunnelScript, "--check")
	if code != 1 || !strings.Contains(out, "drifted  the record of "+labHost) {
		t.Errorf("exit %d\n%s", code, out)
	}
	f.sent()
	out, code = f.run(tunnelScript, "--apply")
	w := writes(f.sent())
	if code != 0 || len(w) != 1 || w[0].Method != "PUT" || !strings.HasSuffix(w[0].Path, "/dns_records/rec0") {
		t.Fatalf("exit %d, wrote:\n%s%s", code, describe(w), out)
	}
	if rec := f.records[0]; rec["content"] != "11111111-2222-4333-8444-555555555555.cfargotunnel.com" || rec["proxied"] != true {
		t.Errorf("the record: %v", rec)
	}
}

// The lab's routes are a file in the repository, so its tunnel is one whose
// routes Cloudflare's dashboard cannot change.
func TestTunnelWhoseRoutesTheDashboardHoldsIsNamedAndLeftAlone(t *testing.T) {
	f := asSpec(t)
	f.tunnels = []map[string]any{{"id": "11111111-2222-4333-8444-555555555555", "name": tunnelName, "config_src": "cloudflare", "status": "healthy"}}
	f.records = []map[string]any{{"id": "rec0", "type": "CNAME", "name": labHost, "content": "11111111-2222-4333-8444-555555555555.cfargotunnel.com", "proxied": true}}
	for _, mode := range []string{"--check", "--apply"} {
		out, code := f.run(tunnelScript, mode)
		if code != 1 || !strings.Contains(out, "drifted  the tunnel "+tunnelName) {
			t.Errorf("%s: exit %d\n%s", mode, code, out)
		}
		if w := writes(f.sent()); len(w) != 0 {
			t.Errorf("%s wrote:\n%s", mode, describe(w))
		}
	}
}

// With user@host:path the credential goes to the mini over ssh and is never
// a file on this machine.
func TestTunnelCredentialGoesOverSSH(t *testing.T) {
	f := asSpec(t)
	far := t.TempDir()
	bin := t.TempDir()
	stub := "#!/bin/bash\necho \"ssh $*\" >> \"$SSH_LOG\"\nwhile [[ $1 == -* ]]; do [[ $1 == -o ]] && shift; shift; done\nshift\ncd \"$SSH_FAR\" && exec /bin/sh -c \"$*\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "ssh.log")
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "SSH_FAR=" + far, "SSH_LOG=" + log}

	out, code := script(t, f, env, tunnelScript, "--apply", "--credential-to", "someone@mini.example:lab-first/tunnel-credential", "--token-file", f.tokenFile())
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	account, tunnel, secret := credential(t, filepath.Join(far, "lab-first", "tunnel-credential"))
	if account != accountID || tunnel != f.tunnels[0]["id"] || secret != f.tunnels[0]["tunnel_secret"] {
		t.Error("the credential that arrived is not this tunnel's")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "someone@mini.example") || strings.Contains(string(calls), secret) {
		t.Errorf("ssh was not asked for the mini, or was given the secret as an argument:\n%s", calls)
	}
	if strings.Contains(out, secret) {
		t.Errorf("the output holds the secret:\n%s", out)
	}

	// A file already there on the mini is never written over.
	f3 := asSpec(t)
	os.WriteFile(filepath.Join(far, "lab-first", "kept"), []byte("an earlier one\n"), 0o600)
	out, code = script(t, f3, env, tunnelScript, "--apply", "--credential-to", "someone@mini.example:lab-first/kept", "--token-file", f3.tokenFile())
	if b, _ := os.ReadFile(filepath.Join(far, "lab-first", "kept")); code == 0 || len(writes(f3.sent())) != 0 || string(b) != "an earlier one\n" {
		t.Errorf("exit %d; want a refusal before any write, and the file as it was\n%s", code, out)
	}

	// A path the far shell would read as more than a path is refused.
	f2 := asSpec(t)
	out, code = script(t, f2, env, tunnelScript, "--apply", "--credential-to", "someone@mini.example:x; rm -rf y", "--token-file", f2.tokenFile())
	if code == 0 || len(writes(f2.sent())) != 0 {
		t.Errorf("exit %d\n%s", code, out)
	}
}
