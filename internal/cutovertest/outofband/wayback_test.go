package outofband

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A mini reached over a made-up SSH: its launchctl keeps whether the
// controller is loaded in a file, and the lab and /status answer from it.
var madeUpMini = map[string]string{
	"lab": `s="$STUBS"
case "$1 $2" in
"status ") echo "NAME STATE" ;;
"status m1-pro") printf 'm1-pro  READY\n  boot:        b1\n' ;;
"status m2-max") printf 'm2-max  READY\n  boot:        b2\n' ;;
"oob m1-pro") if [ -f "$s/unplugged" ]; then echo 'm1-pro: no cable'; else printf 'm1-pro: sighted on the right port\n  right:       connected, product id 29448, plug count 40, shows the name m1-pro: m1-pro\n'; fi ;;
"oob m2-max") echo 'm2-max: sighted on the middle port'
  if [ -f "$s/oobd.loaded" ]; then printf '  controller:  healthy\n  hard reset:  ready\n'; else printf '  controller:  not answering\n  hard reset:  none: the Out-of-band controller is not healthy\n'; fi ;;
"reset m1-pro") echo 'no Hard reset was sent: m1-pro has no Sighting'; exit 1 ;;
"reset m2-max") echo 'no Hard reset was sent: the Out-of-band controller is not healthy'; exit 1 ;;
*) exit 9 ;;
esac`,
	"ssh": `while [ "$1" = -t ]; do shift; done
shift
[ "$1" = sudo ] && shift
[ "$1" = launchctl ] || exit 9
case "$2" in
  bootout) rm -f "$STUBS/oobd.loaded" ;;
  bootstrap) touch "$STUBS/oobd.loaded" ;;
esac`,
	"curl": `if [ -f "$STUBS/oobd.loaded" ]; then
  echo '<dl><dt>Out-of-band controller</dt><dd>answering</dd></dl>'
else
  echo '<dl><dt>Out-of-band controller</dt><dd>not answering since 10:00</dd></dl>'
fi`,
}

// owner plays the person at the machines: at each prompt, in order, it does
// what the step says and answers.
type owner struct {
	mu   sync.Mutex
	out  bytes.Buffer
	seen int
}

func (o *owner) said() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.out.String()
}

func playOwner(t *testing.T, cmd *exec.Cmd, steps []struct {
	prompt string
	do     func()
	answer string
}) (string, int) {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	o := &owner{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		next := 0
		for {
			n, err := r.Read(buf)
			o.mu.Lock()
			o.out.Write(buf[:n])
			text := o.out.String()[o.seen:]
			o.mu.Unlock()
			for next < len(steps) {
				i := strings.Index(text, steps[next].prompt)
				if i < 0 {
					break
				}
				o.mu.Lock()
				o.seen += i + len(steps[next].prompt)
				text = o.out.String()[o.seen:]
				o.mu.Unlock()
				if steps[next].do != nil {
					steps[next].do()
				}
				io.WriteString(stdin, steps[next].answer+"\n")
				next++
			}
			if err != nil {
				return
			}
		}
	}()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(3*time.Minute, func() { cmd.Process.Kill() })
	err = cmd.Wait()
	timer.Stop()
	w.Close()
	<-done
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.said(), code
}

// Test 8 stops the mini's controller. However the run ends after that, a
// "no" to its question included, the controller is started again before
// the record is closed, and the record says so.
func TestOOB08LeavesTheControllerStartedWhateverTheAnswer(t *testing.T) {
	dir, calls := stubs(t, madeUpMini)
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	loadedFile := filepath.Join(dir, "oobd.loaded")
	unplugged := filepath.Join(dir, "unplugged")
	if err := os.WriteFile(loadedFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	records := t.TempDir()
	cmd := exec.Command(script(t, "oob08-refusals"), "--lab", "--mini", "owner@mini", "--records", records)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "LAB_ADDRESS=https://lab.example", "STUBS="+dir)
	out, code := playOwner(t, cmd, []struct {
		prompt string
		do     func()
		answer string
	}{
		{"STOP, by hand: Pull the cable", func() { os.WriteFile(unplugged, nil, 0o644) }, ""},
		{"STOP, by hand: Put the cable", func() { os.Remove(unplugged) }, ""},
		{"Type yes or no: ", nil, "no"},
	})

	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	asked := read(t, calls)
	if !strings.Contains(asked, "launchctl bootout system/com.maclab.oobd") {
		t.Fatalf("the controller was never stopped:\n%s\n%s", out, asked)
	}
	if _, err := os.Stat(loadedFile); err != nil {
		t.Errorf("the run ended with the controller stopped:\n%s\n%s", out, asked)
	}
	files, _ := filepath.Glob(filepath.Join(records, "*-out-of-band-08.md"))
	if len(files) != 1 {
		t.Fatalf("records: %v\n%s", files, out)
	}
	record := read(t, files[0])
	failed := strings.Index(record, "FAILED: \"Agents\" was not told")
	again := strings.Index(record, "the controller was started again on the mini")
	result := strings.Index(record, "Result: fail")
	if failed < 0 || again < failed || result < again {
		t.Errorf("the record does not say the controller was started again before its result:\n%s", record)
	}
}

// Two modes are refused before anything is done, by each test and by run:
// the last one used to win, so "--dry-run --lab" reset real Macs.
func TestTwoModesAreRefused(t *testing.T) {
	dir, calls := stubs(t, nil)
	env := []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "LAB_ADDRESS=https://lab.example"}
	for _, name := range append(append([]string{}, tests...), "run") {
		for _, modes := range [][]string{{"--dry-run", "--lab"}, {"--local", "--lab"}, {"--lab", "--local"}} {
			out, code := run(t, env, script(t, name), append(modes, "--mini", "owner@mini", "--bin", t.TempDir(), "--records", t.TempDir())...)
			if code == 0 || !strings.Contains(out, "two modes") {
				t.Errorf("%s %v: exit %d\n%s", name, modes, code, out)
			}
		}
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("a refused run ran something:\n%s", b)
	}
}
