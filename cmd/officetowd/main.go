// officetowd — local⇄R2 bisync daemon for Office Town wikis.
//
// Scaffold only. Full implementation per:
// https://github.com/jezweb/office-town-cloud/blob/main/.jez/artifacts/officetowd-spec-2026-05-28.md
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.0.1-alpha"

func main() {
	root := &cobra.Command{
		Use:   "officetowd",
		Short: "Local⇄R2 bisync daemon for Office Town wikis",
		Long:  "Watches a local town folder for changes and bisyncs to R2. Goanna-style.",
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print officetowd version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the bisync daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not yet implemented — scaffold only, see V1.1-PLAN §2.1")
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not yet implemented")
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not yet implemented")
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "configure",
		Short: "Interactive config setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not yet implemented")
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "resync",
		Short: "Force a full bisync from scratch",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not yet implemented")
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
