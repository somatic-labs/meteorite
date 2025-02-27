package cmd

import (
	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "meteorite",
	Short: "Meteorite is a testing framework for Cosmos chains",
	Long: `Meteorite is a comprehensive testing framework for Cosmos-based blockchains
that allows you to stress test and validate performance and security claims.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}
