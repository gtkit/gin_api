//go:build !scaffold

package cmd

import "github.com/spf13/cobra"

// addProjectCommand keeps project initialization out of service builds.
func addProjectCommand(*cobra.Command) {}
