package main

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
)

const version = "0.8.4"

var revision = "HEAD"
var showVersion bool
var showHelp bool

func init() {
	pflag.BoolVar(&showVersion, "version", false, "show version information")
	pflag.BoolVarP(&showHelp, "help", "h", false, "show help information")
}

func handleVersion() {
	if showVersion {
		fmt.Printf("tcpulse %s (revision: %s)\n", version, revision)
		os.Exit(0)
	}
}

func handleHelp() {
	if showHelp {
		printUsage()
		os.Exit(0)
	}
}
