//go:build darwin || linux

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// At a real terminal the prompt takes what is typed and shows none of it.
func TestWhatIsTypedAtThePromptIsNotShown(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no pseudo-terminal here:", err)
	}
	defer master.Close()
	name, err := ptsName(master)
	if err != nil {
		t.Skip("no pseudo-terminal here:", err)
	}
	terminal, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no pseudo-terminal here:", err)
	}
	defer terminal.Close()

	var mu sync.Mutex
	var shown bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			shown.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	screen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return shown.String()
	}

	typed := make(chan string, 1)
	go func() {
		got, err := asked(terminal, terminal, "Address of the old site's Postgres (it is not shown): ")
		if err != nil {
			got = "error: " + err.Error()
		}
		typed <- got
	}()
	// The person types once the terminal has stopped echoing.
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := unix.IoctlGetTermios(int(terminal.Fd()), getTermios)
		if err != nil {
			t.Fatal(err)
		}
		if state.Lflag&unix.ECHO == 0 && strings.Contains(screen(), "it is not shown") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the prompt never turned the echo off")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := io.WriteString(master, typedAddress+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-typed:
		if got != typedAddress {
			t.Fatalf("the prompt read something other than what was typed (%d characters)", len(got))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the prompt never returned")
	}
	time.Sleep(100 * time.Millisecond)
	if on := screen(); strings.Contains(on, "hunter2") || strings.Contains(on, "old.site.example") || !strings.Contains(on, "it is not shown") {
		t.Errorf("the terminal showed %q", on)
	}
	if state, err := unix.IoctlGetTermios(int(terminal.Fd()), getTermios); err != nil || state.Lflag&unix.ECHO == 0 {
		t.Errorf("the terminal was left without its echo: %v", err)
	}
}
