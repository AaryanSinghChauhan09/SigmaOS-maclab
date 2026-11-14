package main

import (
	"flag"
	"fmt"
	"os"
)

// Stamped in by the Makefile: the tag of the Lab release this build came
// from, and the release key that release was built with, as "ssh-ed25519
// <base64>", which the install line labd serves pins. A build with no key
// stamped serves the install script as the repository has it.
var (
	version    = "dev"
	releaseKey = ""
)

func init() {
	flag.BoolFunc("version", "print the Lab release this labd came from, and stop", func(string) error {
		fmt.Println(version)
		os.Exit(0)
		return nil
	})
}
