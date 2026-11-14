package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabdReadsItsCopyOfTheControllersTokenFromItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "oobd.token")
	token := strings.Repeat("0123456789abcdef", 4)

	if c, err := outOfBand("http://127.0.0.1:7780", ""); c != nil || err != nil {
		t.Fatalf("with no token file: %v, %v", c, err)
	}
	// Not made yet: labd starts, and reads the file when it has a reset to send.
	c, err := outOfBand("http://127.0.0.1:7780", file)
	if err != nil || c == nil || c.TokenFile != file {
		t.Fatalf("with a token file that is not there yet: %+v, %v", c, err)
	}
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := outOfBand("http://127.0.0.1:7780", file); err != nil || c == nil {
		t.Fatalf("with its own file: %v", err)
	}
	// A copy others can read stops labd.
	if err := os.Chmod(file, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := outOfBand("http://127.0.0.1:7780", file); err == nil || !strings.Contains(err.Error(), "read by others") {
		t.Fatalf("with a file its group can read: %v", err)
	}
	// The controller is on this machine and nowhere else.
	os.Chmod(file, 0o600)
	if _, err := outOfBand("http://192.168.1.20:7780", file); err == nil {
		t.Fatal("a controller on another machine was taken")
	}
}
