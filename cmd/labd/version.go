package main

import (
	"flag"
	"fmt"
	"os"
)

// The tag of the Lab release this build came from, stamped in by the Makefile.
var version = "dev"

func init() {
	flag.BoolFunc("version", "print the Lab release this labd came from, and stop", func(string) error {
		fmt.Println(version)
		os.Exit(0)
		return nil
	})
}
