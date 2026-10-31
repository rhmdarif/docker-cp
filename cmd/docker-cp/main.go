package main

import (
	"fmt"
	"os"
)

// version is set via -ldflags "-X main.version=...".
var version = "dev"

const usage = "usage: docker-cp <version|serve|passwd|setup>"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
	case "serve", "passwd", "setup":
		fmt.Fprintln(os.Stderr, args[0]+": not implemented")
		return 1
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	return 0
}
