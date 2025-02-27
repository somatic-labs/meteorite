package broadcast

import (
	"sync"
	"time"

	"github.com/somatic-labs/meteorite/lib/visualizer"
)

var (
	// Global visualizer instance
	vis         *visualizer.Visualizer
	visInitOnce sync.Once
	visMutex    sync.RWMutex
)

// InitVisualizer initializes the visualizer with RPC endpoints
func InitVisualizer(endpoints []string) error {
	var err error
	visInitOnce.Do(func() {
		vis = visualizer.New(endpoints)
		// Start the visualizer in a goroutine
		go func() {
			err = vis.Start()
		}()
		// Allow time for the visualizer to initialize
		time.Sleep(100 * time.Millisecond)
	})
	return err
}

// UpdateVisualizerStats updates transaction statistics in the visualizer
func UpdateVisualizerStats(successful, failed int, latency time.Duration) {
	visMutex.RLock()
	defer visMutex.RUnlock()

	if vis != nil {
		vis.UpdateTransactionStats(successful, failed)
		vis.RecordLatency(latency)
	}
}

// LogVisualizerDebug adds a debug message to the visualizer
func LogVisualizerDebug(message string) {
	visMutex.RLock()
	defer visMutex.RUnlock()

	if vis != nil {
		vis.AddDebugLog(message)
	}
}

// StopVisualizer stops the visualizer and cleans up resources
func StopVisualizer() {
	visMutex.Lock()
	defer visMutex.Unlock()

	if vis != nil {
		vis.Stop()
		vis = nil
	}
}
