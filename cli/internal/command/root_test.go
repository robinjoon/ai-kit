package command

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootVersion(t *testing.T) {
	var output bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"--version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("execute --version: %v", err)
	}
	if got, want := output.String(), "ctx test-version\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestRootHelp(t *testing.T) {
	var output bytes.Buffer
	root := NewRoot("test-version")
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	if !strings.Contains(output.String(), "Carry development context across coding agents") {
		t.Fatalf("help output does not contain the command summary: %q", output.String())
	}
}
