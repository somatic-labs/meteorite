package cmd

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/somatic-labs/meteorite/lib/chainregistry"
	"github.com/spf13/cobra"
)

// registryCmd represents the registry command
var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Use the Cosmos Chain Registry to test chains",
	Long: `Download and use the Cosmos Chain Registry to test different chains.
This command allows you to:
- Choose a chain from the registry
- Test all available RPC endpoints
- Generate a configuration file for testing
- Run tests using multiple RPCs with different multisend transactions`,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println("Meteorite Chain Registry Tester")
		fmt.Println("==============================")

		// Create a new registry client
		registry := chainregistry.NewRegistry("")

		// Download the registry
		fmt.Println("Downloading the Cosmos Chain Registry...")
		err := registry.Download()
		if err != nil {
			fmt.Printf("Error downloading chain registry: %v\n", err)
			os.Exit(1)
		}

		// Load chains
		fmt.Println("Loading chains from registry...")
		err = registry.LoadChains()
		if err != nil {
			fmt.Printf("Error loading chains: %v\n", err)
			os.Exit(1)
		}

		// Select a chain interactively
		selection, err := chainregistry.SelectChainInteractive(registry)
		if err != nil {
			fmt.Printf("Error selecting chain: %v\n", err)
			os.Exit(1)
		}

		// Generate config
		config, err := chainregistry.GenerateConfigFromChain(selection)
		if err != nil {
			fmt.Printf("Error generating config: %v\n", err)
			os.Exit(1)
		}

		// Save config to file
		fmt.Println("\nGenerating configuration file...")
		configFilename := selection.Chain.ChainName + ".toml"

		// Check if file exists
		if _, err := os.Stat(configFilename); err == nil {
			// Backup existing file
			backupFilename := configFilename + ".bak"
			fmt.Printf("Backing up existing config to %s\n", backupFilename)
			err = os.Rename(configFilename, backupFilename)
			if err != nil {
				fmt.Printf("Error backing up config: %v\n", err)
				os.Exit(1)
			}
		}

		// Create file
		f, err := os.Create(configFilename)
		if err != nil {
			fmt.Printf("Error creating config file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()

		// Write header comment
		_, err = f.WriteString(fmt.Sprintf("# Meteorite configuration for %s (%s)\n",
			selection.Chain.PrettyName, selection.Chain.ChainName))
		if err != nil {
			fmt.Printf("Error writing to config file: %v\n", err)
			os.Exit(1)
		}

		_, err = f.WriteString("# Generated from the Cosmos Chain Registry\n\n")
		if err != nil {
			fmt.Printf("Error writing to config file: %v\n", err)
			os.Exit(1)
		}

		// Encode config
		encoder := toml.NewEncoder(f)
		err = encoder.Encode(config)
		if err != nil {
			fmt.Printf("Error encoding config: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\nConfiguration saved to %s\n", configFilename)
		fmt.Println("\nTo run tests with this configuration:")
		fmt.Printf("1. Ensure you have a seedphrase file in the current directory\n")
		fmt.Printf("2. Run: meteorite -f %s\n", configFilename)
		fmt.Println("\nEach test will send different multisend transactions to different RPC endpoints,")
		fmt.Println("creating unique mempools across the network.")
	},
}

func init() {
	rootCmd.AddCommand(registryCmd)
}
