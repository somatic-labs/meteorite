package main

import (
	"flag"
	"log"

	"github.com/somatic-labs/meteorite/modes/registry"
)

const (
	DefaultVisualizerRefreshMs = 1000
)

func main() {
	log.Println("Welcome to Meteorite - Transaction Scaling Framework for Cosmos SDK chains")

	// Parse command-line flags
	enableViz := flag.Bool("viz", true, "Enable the transaction visualizer")
	flag.Parse()

	// Registry mode is the only mode - no config file needed
	log.Println("Running in zero-configuration registry mode")

	// Pass visualizer setting to registry mode
	if err := registry.RunRegistryMode(*enableViz); err != nil {
		log.Fatalf("Error in registry mode: %v", err)
	}
}
