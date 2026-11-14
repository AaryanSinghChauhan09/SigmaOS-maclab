package minitest

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func scripts(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(repo(t), "scripts", "mini")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	if len(out) == 0 {
		t.Fatal("scripts/mini is empty")
	}
	return out
}

func launchDaemons(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repo(t), "deploy", "*.plist"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no LaunchDaemon file in deploy/: %v", err)
	}
	return files
}

// A daemon run from /opt/homebrew is run from a folder the admin account can
// write (decision S14).
func TestNoLaunchDaemonNamesAPathUnderHomebrew(t *testing.T) {
	for _, f := range launchDaemons(t) {
		if strings.Contains(read(t, f), "/opt/homebrew") {
			t.Errorf("%s names a path under /opt/homebrew", filepath.Base(f))
		}
	}
}

// plist reads the top dictionary of a property list: strings, booleans and
// integers by key, and arrays of strings.
func plist(t *testing.T, file string) (values map[string]string, arrays map[string][]string) {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	values, arrays = map[string]string{}, map[string][]string{}
	d := xml.NewDecoder(f)
	var key, array string
	depth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return values, arrays
		}
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			depth++
			switch name := e.Name.Local; {
			case name == "key" && depth == 3:
				var s string
				d.DecodeElement(&s, &e)
				depth--
				key = s
			case name == "array" && depth == 3:
				array = key
				arrays[array] = []string{}
			case name == "string" || name == "integer":
				var s string
				d.DecodeElement(&s, &e)
				depth--
				if array != "" {
					arrays[array] = append(arrays[array], s)
				} else if depth == 2 {
					values[key] = s
				}
			case (name == "true" || name == "false") && depth == 3:
				values[key] = name
			}
		case xml.EndElement:
			if e.Name.Local == "array" {
				array = ""
			}
			depth--
		}
	}
}

// shell reads NAME="value" or NAME=value from the mini's lib.sh.
func shell(t *testing.T, name string) string {
	t.Helper()
	text := read(t, filepath.Join(repo(t), "scripts", "mini", "lib.sh"))
	m := regexp.MustCompile(`(?m)^` + name + `="?([^"\n]*)"?$`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("scripts/mini/lib.sh sets no %s", name)
	}
	return m[1]
}

func TestLabdsLaunchDaemon(t *testing.T) {
	file := filepath.Join(repo(t), "deploy", labdLabel+".plist")
	values, arrays := plist(t, file)
	for key, want := range map[string]string{
		"Label":     labdLabel,
		"UserName":  "lab",
		"GroupName": "lab",
		"RunAtLoad": "true",
		"KeepAlive": "true",
		"Umask":     "63",
	} {
		if values[key] != want {
			t.Errorf("%s is %q, want %q", key, values[key], want)
		}
	}
	args := arrays["ProgramArguments"]
	if len(args) == 0 || args[0] != "/usr/local/lab/current/bin/labd" {
		t.Fatalf("labd is not run from the build Deploy switched to: %v", args)
	}
	flags := map[string]string{}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			t.Fatalf("argument %q is no flag", args[i])
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			flags[args[i]] = args[i+1]
			i++
		} else {
			flags[args[i]] = ""
		}
	}

	// labd listens on its socket and its loopback port, and Deploy looks for it there.
	for flag, want := range map[string]string{
		"--listen":     shell(t, "LABD_LISTEN"),
		"--socket":     "/var/lab/run/labd.sock",
		"--data":       "/var/lab/data",
		"--public-url": "https://" + shell(t, "LAB_HOST"),
		"--alias":      "maclab.omarchy.org",
		"--sign-in":    "/var/lab/secrets",
	} {
		if got, ok := flags[flag]; !ok || got != want {
			t.Errorf("%s is %q, want %q", flag, got, want)
		}
	}
	if !strings.HasPrefix(flags["--listen"], "127.0.0.1:") {
		t.Errorf("--listen %s is not loopback", flags["--listen"])
	}
	if _, ok := flags["--github-url"]; ok {
		t.Error("the mini's labd is given a stand-in for GitHub")
	}
	if _, ok := flags["--preview"]; !ok {
		t.Error("labd is not started in Preview")
	}

	// Every flag is one this labd has.
	var source strings.Builder
	files, _ := filepath.Glob(filepath.Join(repo(t), "cmd", "labd", "*.go"))
	for _, f := range files {
		source.WriteString(read(t, f))
	}
	for flag := range flags {
		if !regexp.MustCompile(`flag\.\w+\("` + regexp.QuoteMeta(strings.TrimPrefix(flag, "--")) + `"`).MatchString(source.String()) {
			t.Errorf("labd has no flag %s", flag)
		}
	}

	// No secret in a service definition, and its output where lab can write.
	text := read(t, file)
	for _, word := range []string{"TOKEN", "SECRET", "PASSWORD", "EnvironmentVariables"} {
		if strings.Contains(text, word) {
			t.Errorf("labd's LaunchDaemon file holds %s", word)
		}
	}
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if !strings.HasPrefix(values[key], "/var/lab/log/") {
			t.Errorf("%s is %q, outside the lab account's log folder", key, values[key])
		}
	}

	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("plutil", "-lint", file).CombinedOutput(); err != nil {
			t.Errorf("plutil: %v\n%s", err, out)
		}
	}
}

func TestTheMinisScriptsPassShellcheck(t *testing.T) {
	for _, f := range scripts(t) {
		if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %s", filepath.Base(f), out)
		}
	}
	need(t, "shellcheck")
	cmd := exec.Command("shellcheck", append([]string{"--shell=bash", "--external-sources"}, scripts(t)...)...)
	cmd.Dir = repo(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("shellcheck: %v\n%s", err, out)
	}
}

// The mini compiles nothing and has no Python of the lab's; only the set-up
// calls brew, and only through the function that refuses to be root.
func TestTheMinisScriptsNeedOnlyWhatMacOSHas(t *testing.T) {
	banned := regexp.MustCompile(`(^|[^\w./-])(python3?|git|cc|gcc|clang)([^\w./-]|$)`)
	brew := regexp.MustCompile(`(^|[^\w/_-])brew([^\w_-]|$)`)
	for _, f := range scripts(t) {
		name := filepath.Base(f)
		text := read(t, f)
		if !strings.HasPrefix(text, "#!/bin/bash\n") && name != "lib.sh" {
			t.Errorf("%s does not start with macOS's own bash", name)
		}
		for n, line := range strings.Split(text, "\n") {
			code := line
			if i := strings.Index(code, "#"); i >= 0 {
				code = code[:i]
			}
			if m := banned.FindStringSubmatch(code); m != nil {
				t.Errorf("%s:%d needs %s: %s", name, n+1, m[2], strings.TrimSpace(line))
			}
			if brew.MatchString(code) && name != "setup" {
				t.Errorf("%s:%d calls brew: %s", name, n+1, strings.TrimSpace(line))
			}
			if strings.Contains(code, "/bin/brew") && !strings.Contains(text, "brew_as_owner() {") {
				t.Errorf("%s:%d runs brew outside brew_as_owner", name, n+1)
			}
		}
	}
	setup := read(t, filepath.Join(repo(t), "scripts", "mini", "setup"))
	if n := strings.Count(setup, "/bin/brew"); n != 3 {
		t.Errorf("the set-up names brew's binary %d times; want the look for it and the two lines of brew_as_owner", n)
	}
	for _, want := range []string{"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_INSTALL_UPGRADE=1", "brew never runs as root"} {
		if !strings.Contains(setup, want) {
			t.Errorf("the set-up lacks %q", want)
		}
	}
	for _, sub := range []string{"upgrade", "update", "services", "bundle"} {
		if regexp.MustCompile(`brew_as_owner\s+` + sub + `\b`).MatchString(setup) {
			t.Errorf("the set-up runs brew %s", sub)
		}
	}
}

// Deploy checks a bundle as release/verify.sh does: one signer, one namespace.
func TestDeployVerifiesAsTheReleaseScriptsSign(t *testing.T) {
	lib := read(t, filepath.Join(repo(t), "release", "lib.sh"))
	for _, name := range []string{"RELEASE_SIGNER", "RELEASE_NAMESPACE", "RELEASE_BUNDLE"} {
		m := regexp.MustCompile(`(?m)^` + name + `="([^"]*)"$`).FindStringSubmatch(lib)
		if m == nil {
			t.Fatalf("release/lib.sh sets no %s", name)
		}
		if got := shell(t, name); got != m[1] {
			t.Errorf("%s is %q on the mini and %q in release/lib.sh", name, got, m[1])
		}
	}
	if shell(t, "RELEASE_SIGNER") != signer || shell(t, "RELEASE_NAMESPACE") != namespace || shell(t, "RELEASE_BUNDLE") != bundleName {
		t.Error("the tests sign under other names than the scripts verify")
	}
	if shell(t, "LABD_LABEL") != labdLabel {
		t.Errorf("LABD_LABEL is %q", shell(t, "LABD_LABEL"))
	}
}

// Outside the tests, what stands in for / and for root is not there to be set.
func TestTheStandInsNeedTheTestsRoot(t *testing.T) {
	lib := read(t, filepath.Join(repo(t), "scripts", "mini", "lib.sh"))
	for _, want := range []string{`ROOT_OWNER=root`, `ROOT_GROUP=wheel`, `LAB_OWNER=lab`, `PATH=/usr/bin:/bin:/usr/sbin:/sbin`} {
		if !strings.Contains(lib, want) {
			t.Errorf("scripts/mini/lib.sh lacks %s", want)
		}
	}
}
