package main

import (
	"os"

	"github.com/robinjoon/ai-kit/cli/internal/command"
)

var version = "dev"

func main() {
	os.Exit(command.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version))
}
