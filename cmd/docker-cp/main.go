package main

import (
	"fmt"
	"os"

	"docker-cp/internal/config"
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
	case "passwd", "setup":
		cfg := config.FromEnv(config.Default())
		cli := runPasswd
		if args[0] == "setup" {
			cli = runSetup
		}
		if err := cli(cfg, args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", args[0], err)
			return 1
		}
	case "serve":
		fmt.Fprintln(os.Stderr, args[0]+": not implemented")
		return 1
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	return 0
}
