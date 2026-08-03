package command

import "github.com/spf13/cobra"

func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "ctx",
		Short:         "Carry development context across coding agents",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("ctx {{.Version}}\n")
	return root
}
