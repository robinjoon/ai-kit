package main

import (
	"fmt"
	"os"

	"github.com/robinjoon/ai-kit/cli/internal/command"
)

var version = "dev"

func main() {
	root := command.NewRoot(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
