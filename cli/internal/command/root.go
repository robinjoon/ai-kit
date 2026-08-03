// Package command maps the stable ctx process contract onto app use-cases.
package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/robinjoon/ai-kit/cli/internal/app"
	"github.com/robinjoon/ai-kit/cli/internal/schema"
	"github.com/robinjoon/ai-kit/cli/internal/store"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	ExitUsage      = 2
	ExitNotFound   = 3
	ExitAmbiguous  = 4
	ExitValidation = 5
	ExitGit        = 6
	ExitStore      = 7
	ExitSync       = 8
)

var errUsage = errors.New("ctx command usage")

type options struct {
	cwd       string
	storeRoot string
	client    string
	sessionID string
	json      bool
	version   string
}

func NewRoot(version string) *cobra.Command {
	opts := &options{version: version}
	root := &cobra.Command{
		Use:           "ctx",
		Short:         "Carry development context across coding agents",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetVersionTemplate("ctx {{.Version}}\n")
	root.Version = version
	root.PersistentFlags().StringVar(&opts.cwd, "cwd", "", "working directory to observe")
	root.PersistentFlags().StringVar(&opts.storeRoot, "store", "", "ctx sidecar root (default: user config directory)")
	root.PersistentFlags().StringVar(&opts.client, "client", "ctx.cli", "calling client identifier")
	root.PersistentFlags().StringVar(&opts.sessionID, "session-id", "", "calling client session identifier")
	root.PersistentFlags().BoolVar(&opts.json, "json", false, "write a versioned JSON response")
	root.AddCommand(newTaskCommand(opts), newRepoCommand(opts), newResolveCommand(opts), newCheckpointCommand(opts), newHandoffCommand(opts), newResumeCommand(opts), newStatusCommand(opts), newSnapshotCommand(opts), newSyncCommand(opts))
	return root
}

func newRepoCommand(opts *options) *cobra.Command {
	repo := &cobra.Command{Use: "repo", Short: "Manage explicit ctx repository identity links"}
	var fromRepoID string
	link := &cobra.Command{
		Use:   "link",
		Short: "Link a previous local repository ID to the current Git remote identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fromRepoID == "" {
				return fmt.Errorf("%w: --from is required", errUsage)
			}
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			result, err := service.LinkRepository(cmd.Context(), cwd(opts), fromRepoID)
			if err != nil {
				return err
			}
			return writeResult(cmd, opts, "repo.link", result)
		},
	}
	link.Flags().StringVar(&fromRepoID, "from", "", "previous local repository ID")
	repo.AddCommand(link)
	return repo
}

func newTaskCommand(opts *options) *cobra.Command {
	task := &cobra.Command{Use: "task", Short: "Create, list, and select ctx tasks"}
	var title string
	var aliases []string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a task and bind it to this client",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if title == "" {
				return fmt.Errorf("%w: --title is required", errUsage)
			}
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			result, err := service.CreateTask(cmd.Context(), cwd(opts), title, aliases)
			if err != nil {
				return err
			}
			return writeResult(cmd, opts, "task.create", result)
		},
	}
	create.Flags().StringVar(&title, "title", "", "task title")
	create.Flags().StringSliceVar(&aliases, "alias", nil, "human-readable task alias (repeatable)")
	var switchSelector string
	switchCmd := &cobra.Command{
		Use:   "switch <task-id-or-alias>",
		Short: "Bind this client to an existing task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switchSelector = args[0]
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			result, err := service.SwitchTask(cmd.Context(), cwd(opts), switchSelector)
			if err != nil {
				return err
			}
			return writeResult(cmd, opts, "task.switch", result)
		},
	}
	list := &cobra.Command{
		Use: "list", Short: "List tasks in the current repository", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			result, err := service.ListTasks(cmd.Context(), cwd(opts))
			if err != nil {
				return err
			}
			return writeResult(cmd, opts, "task.list", result)
		},
	}
	task.AddCommand(create, list, switchCmd)
	return task
}

func newResolveCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "resolve", Short: "Resolve repository and active task bindings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := serviceFor(opts)
		if err != nil {
			return err
		}
		result, err := service.Resolve(cmd.Context(), cwd(opts))
		if err != nil {
			return err
		}
		return writeResult(cmd, opts, "resolve", result)
	}}
}

func newCheckpointCommand(opts *options) *cobra.Command {
	var inputPath, purpose string
	var parents []string
	command := &cobra.Command{
		Use: "checkpoint", Short: "Create an immutable semantic checkpoint", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input, err := readInput(cmd, inputPath)
			if err != nil {
				return err
			}
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			result, err := service.Checkpoint(cmd.Context(), app.CheckpointRequest{CWD: cwd(opts), Purpose: purpose, Parents: parents, Input: input})
			if err != nil {
				return err
			}
			return writeResult(cmd, opts, "checkpoint", result)
		},
	}
	command.Flags().StringVar(&inputPath, "input", "", "capture input JSON file, or - for stdin")
	command.Flags().StringVar(&purpose, "purpose", "progress", "progress, milestone, completion, merge, recovery")
	command.Flags().StringSliceVar(&parents, "parent", nil, "parent checkpoint ID (repeatable; merge needs at least two)")
	return command
}

func newHandoffCommand(opts *options) *cobra.Command {
	var inputPath, targetSystem, targetInterface, remote string
	var parents []string
	var syncAfter bool
	command := &cobra.Command{
		Use: "handoff", Short: "Create a stable checkpoint and its thin handoff pointer", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if syncAfter && remote == "" {
				return fmt.Errorf("%w: --remote is required with --sync", errUsage)
			}
			input, err := readInput(cmd, inputPath)
			if err != nil {
				return err
			}
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			target := app.Record{}
			if targetSystem != "" {
				target["system"] = targetSystem
			}
			if targetInterface != "" {
				target["interface"] = targetInterface
			}
			result, err := service.Handoff(cmd.Context(), app.CheckpointRequest{CWD: cwd(opts), Parents: parents, Input: input}, target)
			if err != nil {
				return err
			}
			if syncAfter {
				if err := service.Sync(cmd.Context(), cwd(opts), remote, "push"); err != nil {
					return err
				}
			}
			return writeResult(cmd, opts, "handoff", result)
		},
	}
	command.Flags().StringVar(&inputPath, "input", "", "complete capture input JSON file, or - for stdin")
	command.Flags().StringSliceVar(&parents, "parent", nil, "parent checkpoint ID (at most one)")
	command.Flags().StringVar(&targetSystem, "target-system", "", "target agent system identifier")
	command.Flags().StringVar(&targetInterface, "target-interface", "", "target interface")
	command.Flags().BoolVar(&syncAfter, "sync", false, "push immutable checkpoints after handoff")
	command.Flags().StringVar(&remote, "remote", "", "filesystem ctx store used with --sync")
	return command
}

func newResumeCommand(opts *options) *cobra.Command {
	var taskSelector, checkpointID, remote string
	var maxBytes int
	var syncFirst bool
	command := &cobra.Command{
		Use: "resume", Short: "Render a self-contained task continuation", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if maxBytes < 0 {
				return fmt.Errorf("%w: --max-bytes must be zero or greater", errUsage)
			}
			if syncFirst && remote == "" {
				return fmt.Errorf("%w: --remote is required with --sync", errUsage)
			}
			service, err := serviceFor(opts)
			if err != nil {
				return err
			}
			if syncFirst {
				if err := service.Sync(cmd.Context(), cwd(opts), remote, "pull"); err != nil {
					return err
				}
			}
			result, err := service.Resume(cmd.Context(), cwd(opts), taskSelector, checkpointID, maxBytes)
			if err != nil {
				return err
			}
			if opts.json {
				return writeResult(cmd, opts, "resume", map[string]any{"task_id": result.TaskID, "checkpoint_id": result.CheckpointID, "content": string(result.Content)})
			}
			_, err = cmd.OutOrStdout().Write(result.Content)
			return err
		},
	}
	command.Flags().StringVar(&taskSelector, "task", "", "task ID or alias")
	command.Flags().StringVar(&checkpointID, "checkpoint", "", "checkpoint ID")
	command.Flags().IntVar(&maxBytes, "max-bytes", 32*1024, "maximum markdown output bytes; zero disables the limit")
	command.Flags().BoolVar(&syncFirst, "sync", false, "pull immutable checkpoints before resuming")
	command.Flags().StringVar(&remote, "remote", "", "filesystem ctx store used with --sync")
	return command
}

func newStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show task, checkpoint graph, and Git state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := serviceFor(opts)
		if err != nil {
			return err
		}
		result, err := service.Status(cmd.Context(), cwd(opts))
		if err != nil {
			return err
		}
		return writeResult(cmd, opts, "status", result)
	}}
}

func newSnapshotCommand(opts *options) *cobra.Command {
	var kind, name string
	command := &cobra.Command{Use: "snapshot", Short: "Store a mechanical Git and session observation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := serviceFor(opts)
		if err != nil {
			return err
		}
		result, err := service.Snapshot(cmd.Context(), cwd(opts), kind, name)
		if err != nil {
			return err
		}
		return writeResult(cmd, opts, "snapshot", result)
	}}
	command.Flags().StringVar(&kind, "trigger", "manual", "manual, ctx-command, lifecycle-hook, recovery, or other")
	command.Flags().StringVar(&name, "name", "", "trigger name when required")
	return command
}

func newSyncCommand(opts *options) *cobra.Command {
	var remote, direction string
	command := &cobra.Command{Use: "sync", Short: "Union immutable checkpoints with a filesystem store", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if remote == "" {
			return fmt.Errorf("%w: --remote is required", errUsage)
		}
		service, err := serviceFor(opts)
		if err != nil {
			return err
		}
		if err := service.Sync(cmd.Context(), cwd(opts), remote, direction); err != nil {
			return err
		}
		return writeResult(cmd, opts, "sync", map[string]any{"remote": remote, "direction": direction})
	}}
	command.Flags().StringVar(&remote, "remote", "", "remote filesystem ctx store root")
	command.Flags().StringVar(&direction, "direction", "both", "both, pull, or push")
	return command
}

func serviceFor(opts *options) (*app.Service, error) {
	root := opts.storeRoot
	if root == "" {
		var err error
		root, err = store.DefaultRoot()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", app.ErrStore, err)
		}
	}
	return app.New(store.New(root), app.Config{Client: opts.client, SessionID: opts.sessionID, Version: opts.version}), nil
}

func cwd(opts *options) string {
	if opts.cwd != "" {
		return opts.cwd
	}
	value, err := os.Getwd()
	if err != nil {
		return "."
	}
	return value
}

func readInput(cmd *cobra.Command, path string) (app.Record, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: --input is required", errUsage)
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read input: %v", app.ErrValidation, err)
	}
	record, err := schema.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w: input must be one JSON object: %v", app.ErrValidation, err)
	}
	return record, nil
}

func writeResult(cmd *cobra.Command, opts *options, name string, value any) error {
	if opts.json {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"output_version": 1, "command": name, "data": value})
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

// ExitCode maps expected failures to the stable process contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var (
		flagNotExist      *pflag.NotExistError
		flagValueRequired *pflag.ValueRequiredError
		flagInvalidValue  *pflag.InvalidValueError
		flagInvalidSyntax *pflag.InvalidSyntaxError
	)
	switch {
	case errors.Is(err, errUsage):
		return ExitUsage
	case errors.As(err, &flagNotExist), errors.As(err, &flagValueRequired), errors.As(err, &flagInvalidValue), errors.As(err, &flagInvalidSyntax):
		return ExitUsage
	case errors.Is(err, app.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, app.ErrAmbiguous):
		return ExitAmbiguous
	case errors.Is(err, app.ErrValidation):
		return ExitValidation
	case errors.Is(err, app.ErrGit):
		return ExitGit
	case errors.Is(err, app.ErrStore):
		return ExitStore
	case errors.Is(err, app.ErrSync):
		return ExitSync
	case strings.Contains(err.Error(), "unknown command"), strings.Contains(err.Error(), "requires"), strings.Contains(err.Error(), "required flag"), strings.Contains(err.Error(), "accepts "), strings.Contains(err.Error(), "flag needs an argument"), strings.Contains(err.Error(), "unknown flag"):
		return ExitUsage
	default:
		return 1
	}
}
