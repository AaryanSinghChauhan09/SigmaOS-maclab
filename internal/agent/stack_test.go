package agent

import (
	"maps"
	"testing"
)

// The stack a Mac reports is pacman -Q's lines for the stack's packages only.
func TestStackOfPacmanQ(t *testing.T) {
	out := "bash 5.2.037-5\nlinux-aurora 6.17.5.aurora1-1\nlinux-aurora-headers 6.17.5.aurora1-1\nm1n1-aurora 1.5.2-1\n" +
		"mesa 1:25.3.0-1\nfirefox 145.0-1\nuboot-asahi 2025.10-1\nasahi-fwextract 0.7.9-1\nnot a line\n"
	want := map[string]string{"linux-aurora": "6.17.5.aurora1-1", "linux-aurora-headers": "6.17.5.aurora1-1",
		"m1n1-aurora": "1.5.2-1", "mesa": "1:25.3.0-1", "uboot-asahi": "2025.10-1", "asahi-fwextract": "0.7.9-1"}
	if got := stackOf([]byte(out)); !maps.Equal(got, want) {
		t.Fatalf("stack %v, want %v", got, want)
	}
	if got := stackOf(nil); got != nil {
		t.Fatalf("no pacman: %v", got)
	}
}
