package command

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/robinjoon/ai-kit/cli/internal/core"
)

type globalOptions struct {
	cwd     string
	store   string
	client  string
	json    bool
	version bool
	help    bool
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	opts, commandName, commandArgs, code := parseGlobal(args, stderr)
	if code != 0 {
		return code
	}
	if opts.version {
		fmt.Fprintf(stdout, "ctx %s\n", version)
		return 0
	}
	if opts.help {
		printHelp(stdout)
		return 0
	}
	if commandName == "" {
		printHelp(stdout)
		return 0
	}

	service, err := core.New(opts.store, opts.client)
	if err != nil {
		return fail(stderr, err)
	}

	switch commandName {
	case "start":
		return runStart(service, opts, commandArgs, stdout, stderr)
	case "checkpoint":
		return runCheckpoint(service, opts, commandArgs, stdin, stdout, stderr)
	case "resume":
		return runResume(service, opts, commandArgs, stdout, stderr)
	case "status":
		return runStatus(service, opts, commandArgs, stdout, stderr)
	case "help":
		printHelp(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "ctx: unknown command %q\n", commandName)
		return 2
	}
}

func parseGlobal(args []string, stderr io.Writer) (globalOptions, string, []string, int) {
	opts := globalOptions{client: "ctx.cli"}
	flags := flag.NewFlagSet("ctx", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.cwd, "cwd", "", "Git working directory")
	flags.StringVar(&opts.store, "store", "", "local ctx store")
	flags.StringVar(&opts.client, "client", opts.client, "calling agent")
	flags.BoolVar(&opts.json, "json", false, "write JSON output")
	flags.BoolVar(&opts.version, "version", false, "print version")
	flags.BoolVar(&opts.version, "v", false, "print version")
	flags.BoolVar(&opts.help, "help", false, "show help")
	flags.BoolVar(&opts.help, "h", false, "show help")
	flags.Usage = func() { printHelp(stderr) }
	if err := flags.Parse(args); err != nil {
		return opts, "", nil, 2
	}
	rest := flags.Args()
	if len(rest) == 0 {
		return opts, "", nil, 0
	}
	return opts, rest[0], rest[1:], 0
}

func runStart(service *core.Service, opts globalOptions, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ctx start", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var title string
	flags.StringVar(&title, "title", "", "work title")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	result, err := service.Start(opts.cwd, title)
	if err != nil {
		return fail(stderr, err)
	}
	if opts.json {
		return writeJSON(stdout, stderr, "start", result)
	}
	fmt.Fprintf(stdout, "Started: %s\nWorktree: %s\nBranch: %s\nContext: %s\nCheckpoint: %s\n", result.Active.Title, result.Scope.WorktreeRoot, result.Scope.Branch, result.Active.ContextID, result.Checkpoint.ID)
	return 0
}

func runCheckpoint(service *core.Service, opts globalOptions, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ctx checkpoint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var inputPath, reason string
	flags.StringVar(&inputPath, "input", "", "checkpoint JSON path, or - for stdin")
	flags.StringVar(&reason, "reason", "progress", "short capture reason")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	input, err := readCheckpointInput(inputPath, stdin)
	if err != nil {
		return fail(stderr, err)
	}
	checkpoint, err := service.Checkpoint(opts.cwd, reason, input)
	if err != nil {
		return fail(stderr, err)
	}
	if opts.json {
		return writeJSON(stdout, stderr, "checkpoint", checkpoint)
	}
	fmt.Fprintf(stdout, "Saved checkpoint: %s\n", checkpoint.ID)
	return 0
}

func runResume(service *core.Service, opts globalOptions, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "ctx resume: no arguments expected")
		return 2
	}
	state, err := service.Resume(opts.cwd)
	if err != nil {
		return fail(stderr, err)
	}
	if opts.json {
		return writeJSON(stdout, stderr, "resume", state)
	}
	renderResume(stdout, state)
	return 0
}

func runStatus(service *core.Service, opts globalOptions, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "ctx status: no arguments expected")
		return 2
	}
	state, err := service.Status(opts.cwd)
	if err != nil {
		return fail(stderr, err)
	}
	if opts.json {
		return writeJSON(stdout, stderr, "status", state)
	}
	fmt.Fprintf(stdout, "Worktree: %s\nBranch: %s\nContext: %s\nTitle: %s\nLatest checkpoint: %s\nUpdated: %s\n", state.Scope.WorktreeRoot, state.Scope.Branch, state.Active.ContextID, state.Active.Title, state.Latest.ID, state.Active.UpdatedAt.Format("2006-01-02 15:04:05Z07:00"))
	if len(state.Differences) == 0 {
		fmt.Fprintln(stdout, "Git: unchanged since checkpoint")
	} else {
		fmt.Fprintln(stdout, "Git differences:")
		writeList(stdout, state.Differences)
	}
	return 0
}

func readCheckpointInput(path string, stdin io.Reader) (core.CheckpointInput, error) {
	var input core.CheckpointInput
	if path == "" {
		return input, errors.New("--input is required")
	}
	reader := stdin
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return input, fmt.Errorf("open checkpoint input: %w", err)
		}
		defer file.Close()
		reader = file
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("decode checkpoint input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return input, errors.New("checkpoint input must contain exactly one JSON object")
	}
	return input, nil
}

func renderResume(out io.Writer, state core.State) {
	ctx := state.Latest.Context
	fmt.Fprintf(out, "# Resume: %s\n\n", state.Active.Title)
	fmt.Fprintf(out, "Worktree: `%s`\nBranch: `%s`\nCheckpoint: `%s`\nSaved by: `%s`\n\n", state.Scope.WorktreeRoot, state.Scope.Branch, state.Latest.ID, state.Latest.Client)
	fmt.Fprintf(out, "## Goal\n%s\n\n## Current summary\n%s\n", ctx.Goal, ctx.Summary)
	writeDecisionsSection(out, ctx.Decisions)
	writeSection(out, "Next actions", ctx.NextActions)
	writeSection(out, "Blockers", ctx.Blockers)
	if len(state.Differences) > 0 {
		fmt.Fprintln(out, "\n## Git differences")
		writeList(out, state.Differences)
	}
}

func writeDecisionsSection(out io.Writer, decisions []core.Decision) {
	if len(decisions) == 0 {
		return
	}
	fmt.Fprintln(out, "\n## Decisions")
	for _, d := range decisions {
		if d.Why != "" {
			fmt.Fprintf(out, "- %s\n  - Why: %s\n", d.What, d.Why)
		} else {
			fmt.Fprintf(out, "- %s\n", d.What)
		}
	}
}

func writeSection(out io.Writer, title string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(out, "\n## %s\n", title)
	writeList(out, values)
}

func writeList(out io.Writer, values []string) {
	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", value)
	}
}

func writeJSON(out, stderr io.Writer, command string, data any) int {
	response := struct {
		OutputVersion int    `json:"output_version"`
		Command       string `json:"command"`
		Data          any    `json:"data"`
	}{OutputVersion: 1, Command: command, Data: data}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "ctx: %v\n", err)
	return 1
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, strings.TrimSpace(`ctx carries the latest work context between Claude Code and Codex.

Usage:
  ctx [global options] start --title <title>
  ctx [global options] checkpoint --input <file|-> [--reason <reason>]
  ctx [global options] resume
  ctx [global options] status

Global options:
  --cwd <directory>   Git working directory
  --store <directory> local ctx store
  --client <name>     calling agent
  --json              write JSON output
  --version, -v       print version`))
}
