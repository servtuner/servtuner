// Command servtuner benchmarks LLM serving stacks: it runs one benchmark, analyzes and
// compares runs, plans capacity, and starts vLLM on a host as the launcher.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/servtuner/servtuner/bench/internal/cli"
	"github.com/servtuner/servtuner/bench/internal/memlimit"
)

func main() {
	// Go reads the container's CPU quota on its own but not its memory limit.
	if _, err := memlimit.ApplySoftLimit(memlimit.DefaultCgroupRoot, os.Getenv, debug.SetMemoryLimit); err != nil {
		fmt.Fprintf(os.Stderr, "%sread the container memory limit: %v\n", cli.ErrorPrefix, err)
	}
	os.Exit(cli.Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
