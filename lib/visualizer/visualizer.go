package visualizer

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cometbft/cometbft/rpc/client/http"
	"github.com/fatih/color"
	"github.com/somatic-labs/meteorite/lib/peerdiscovery"
)

// MempoolStats contains mempool information for a node
type MempoolStats struct {
	NodeURL        string
	TxCount        int
	TxBytes        int64
	PendingTxs     int
	LastUpdateTime time.Time
}

// TransactionStats contains transaction processing statistics
type TransactionStats struct {
	TotalTxs       int64
	SuccessfulTxs  int64
	FailedTxs      int64
	TPS            float64
	AverageLatency time.Duration
	RecentLatency  []time.Duration // Last N latencies
	BlockHeight    int64
}

// NodeStatus contains status information for a node
type NodeStatus struct {
	NodeURL          string
	IsOnline         bool
	BlockHeight      int64
	BlockTime        time.Time
	PeerCount        int
	LastCheckedTime  time.Time
	CatchingUp       bool
	ValidatorAddress string
}

// Visualizer manages the terminal UI for transaction monitoring
type Visualizer struct {
	txStats       *TransactionStats
	mempoolStats  map[string]*MempoolStats
	nodeStatus    map[string]*NodeStatus
	statsMutex    sync.RWMutex
	stopChan      chan struct{}
	discovery     *peerdiscovery.PeerDiscovery
	chartData     []float64
	chartMaxItems int
	debugLog      []string
	ticker        *time.Ticker
	logFile       *os.File
}

// New creates a new visualizer instance
func New(endpoints []string) *Visualizer {
	// Create logs directory if it doesn't exist
	os.MkdirAll("logs", 0755)

	// Create a log file with timestamp
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	logFile, err := os.Create(fmt.Sprintf("logs/meteorite_viz_%s.log", timestamp))
	if err != nil {
		fmt.Printf("Warning: could not create log file: %v\n", err)
	}

	v := &Visualizer{
		txStats:       &TransactionStats{RecentLatency: make([]time.Duration, 0, 100)},
		mempoolStats:  make(map[string]*MempoolStats),
		nodeStatus:    make(map[string]*NodeStatus),
		stopChan:      make(chan struct{}),
		discovery:     peerdiscovery.New(endpoints),
		chartData:     make([]float64, 0, 60),
		chartMaxItems: 60, // 1 minute of data at 1 second refresh
		debugLog:      make([]string, 0, 100),
		logFile:       logFile,
	}

	// Print header
	greenBold := color.New(color.FgGreen, color.Bold).SprintFunc()
	fmt.Println("")
	fmt.Println(greenBold("==============================================="))
	fmt.Println(greenBold("  METEORITE") + " Transaction Visualizer Started")
	fmt.Println(greenBold("==============================================="))
	fmt.Println("")

	if logFile != nil {
		fmt.Fprintf(logFile, "===============================================\n")
		fmt.Fprintf(logFile, "  METEORITE Transaction Visualizer Started\n")
		fmt.Fprintf(logFile, "  Time: %s\n", time.Now().Format(time.RFC3339))
		fmt.Fprintf(logFile, "===============================================\n\n")
	}

	return v
}

// Start begins the visualization
func (v *Visualizer) Start() error {
	v.ticker = time.NewTicker(5 * time.Second)

	// Start background data collection
	go v.collectData()

	return nil
}

// Stop terminates the visualization
func (v *Visualizer) Stop() {
	if v.ticker != nil {
		v.ticker.Stop()
	}

	close(v.stopChan)
	v.discovery.Cleanup()

	// Dump final stats
	v.printStats()

	// Close log file
	if v.logFile != nil {
		fmt.Fprintf(v.logFile, "\n===============================================\n")
		fmt.Fprintf(v.logFile, "  METEORITE Visualizer Stopped\n")
		fmt.Fprintf(v.logFile, "  Time: %s\n", time.Now().Format(time.RFC3339))
		fmt.Fprintf(v.logFile, "===============================================\n")
		v.logFile.Close()
	}

	blueBold := color.New(color.FgBlue, color.Bold).SprintFunc()
	fmt.Println("")
	fmt.Println(blueBold("==============================================="))
	fmt.Println(blueBold("  METEORITE") + " Transaction Visualizer Stopped")
	fmt.Println(blueBold("==============================================="))
	if v.logFile != nil {
		fmt.Println("Log file saved to:", v.logFile.Name())
	}
}

// UpdateTransactionStats updates transaction statistics
func (v *Visualizer) UpdateTransactionStats(successful, failed int) {
	v.statsMutex.Lock()
	defer v.statsMutex.Unlock()

	v.txStats.TotalTxs += int64(successful + failed)
	v.txStats.SuccessfulTxs += int64(successful)
	v.txStats.FailedTxs += int64(failed)

	// Calculate new TPS based on last minute
	v.chartData = append(v.chartData, float64(successful))
	if len(v.chartData) > v.chartMaxItems {
		v.chartData = v.chartData[1:]
	}

	// Calculate TPS as moving average
	var sum float64
	for _, val := range v.chartData {
		sum += val
	}

	if len(v.chartData) > 0 {
		v.txStats.TPS = sum / float64(len(v.chartData))
	}
}

// RecordLatency records a transaction latency
func (v *Visualizer) RecordLatency(latency time.Duration) {
	v.statsMutex.Lock()
	defer v.statsMutex.Unlock()

	v.txStats.RecentLatency = append(v.txStats.RecentLatency, latency)
	if len(v.txStats.RecentLatency) > 100 {
		v.txStats.RecentLatency = v.txStats.RecentLatency[1:]
	}

	// Calculate average
	var total time.Duration
	for _, l := range v.txStats.RecentLatency {
		total += l
	}

	if len(v.txStats.RecentLatency) > 0 {
		v.txStats.AverageLatency = total / time.Duration(len(v.txStats.RecentLatency))
	}
}

// UpdateMempoolStats updates mempool statistics for a node
func (v *Visualizer) UpdateMempoolStats(nodeURL string, txCount int, txBytes int64, pendingTxs int) {
	v.statsMutex.Lock()
	defer v.statsMutex.Unlock()

	stats, exists := v.mempoolStats[nodeURL]
	if !exists {
		stats = &MempoolStats{NodeURL: nodeURL}
		v.mempoolStats[nodeURL] = stats
	}

	stats.TxCount = txCount
	stats.TxBytes = txBytes
	stats.PendingTxs = pendingTxs
	stats.LastUpdateTime = time.Now()
}

// UpdateNodeStatus updates status information for a node
func (v *Visualizer) UpdateNodeStatus(nodeURL string, height int64, blockTime time.Time,
	peerCount int, catchingUp bool, validatorAddress string) {

	v.statsMutex.Lock()
	defer v.statsMutex.Unlock()

	status, exists := v.nodeStatus[nodeURL]
	if !exists {
		status = &NodeStatus{NodeURL: nodeURL}
		v.nodeStatus[nodeURL] = status
	}

	status.IsOnline = true
	status.BlockHeight = height
	status.BlockTime = blockTime
	status.PeerCount = peerCount
	status.LastCheckedTime = time.Now()
	status.CatchingUp = catchingUp
	status.ValidatorAddress = validatorAddress

	// Also update tx stats
	if height > v.txStats.BlockHeight {
		v.txStats.BlockHeight = height
	}
}

// MarkNodeOffline marks a node as offline
func (v *Visualizer) MarkNodeOffline(nodeURL string) {
	v.statsMutex.Lock()
	defer v.statsMutex.Unlock()

	status, exists := v.nodeStatus[nodeURL]
	if !exists {
		status = &NodeStatus{NodeURL: nodeURL}
		v.nodeStatus[nodeURL] = status
	}

	status.IsOnline = false
	status.LastCheckedTime = time.Now()
}

// AddDebugLog adds a message to the debug log
func (v *Visualizer) AddDebugLog(message string) {
	v.statsMutex.Lock()
	defer v.statsMutex.Unlock()

	timestamp := time.Now().Format("15:04:05.000")
	logEntry := fmt.Sprintf("[%s] %s", timestamp, message)

	v.debugLog = append(v.debugLog, logEntry)
	if len(v.debugLog) > 100 {
		v.debugLog = v.debugLog[1:]
	}

	// Print to console with color
	fmt.Printf("%s %s\n", color.YellowString("[%s]", timestamp), message)

	// Log to file
	if v.logFile != nil {
		fmt.Fprintf(v.logFile, "%s\n", logEntry)
	}
}

// collectData periodically collects data from nodes
func (v *Visualizer) collectData() {
	// Initial data collection
	go v.collectMempoolData()
	go v.discoverNodes()

	for {
		select {
		case <-v.stopChan:
			return
		case <-v.ticker.C:
			v.printStats()
			go v.collectMempoolData()
		}
	}
}

// collectMempoolData collects mempool statistics from each known node
func (v *Visualizer) collectMempoolData() {
	v.statsMutex.RLock()
	nodes := make([]string, 0, len(v.nodeStatus))
	for url := range v.nodeStatus {
		nodes = append(nodes, url)
	}
	v.statsMutex.RUnlock()

	for _, url := range nodes {
		go func(nodeURL string) {
			rpcClient, err := http.New(nodeURL, "/websocket")
			if err != nil {
				v.MarkNodeOffline(nodeURL)
				return
			}

			// Get mempool info
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			unconfirmedTxs, err := rpcClient.UnconfirmedTxs(ctx, nil)
			if err != nil {
				v.MarkNodeOffline(nodeURL)
				return
			}

			// Get node status
			status, err := rpcClient.Status(ctx)
			if err != nil {
				v.MarkNodeOffline(nodeURL)
				return
			}

			// Update mempool stats
			v.UpdateMempoolStats(
				nodeURL,
				unconfirmedTxs.Total,
				unconfirmedTxs.TotalBytes,
				unconfirmedTxs.Total,
			)

			// Get peer count - we need to check if the field exists
			peerCount := 0
			netInfo, err := rpcClient.NetInfo(ctx)
			if err == nil {
				peerCount = len(netInfo.Peers)
			}

			// Update node status
			v.UpdateNodeStatus(
				nodeURL,
				status.SyncInfo.LatestBlockHeight,
				status.SyncInfo.LatestBlockTime,
				peerCount,
				status.SyncInfo.CatchingUp,
				status.ValidatorInfo.Address.String(),
			)
		}(url)
	}
}

// discoverNodes discovers new nodes in the network
func (v *Visualizer) discoverNodes() {
	v.AddDebugLog("Starting node discovery...")

	// Use a 30-second timeout for discovery
	endpoints, err := v.discovery.DiscoverPeers(30 * time.Second)
	if err != nil {
		v.AddDebugLog(fmt.Sprintf("Discovery error: %v", err))
		return
	}

	count := 0
	for _, endpoint := range endpoints {
		v.statsMutex.RLock()
		_, exists := v.nodeStatus[endpoint]
		v.statsMutex.RUnlock()

		if !exists {
			count++
			// Add as unknown node, will be updated in next collection cycle
			v.statsMutex.Lock()
			v.nodeStatus[endpoint] = &NodeStatus{
				NodeURL:         endpoint,
				LastCheckedTime: time.Now(),
			}
			v.statsMutex.Unlock()
		}
	}

	v.AddDebugLog(fmt.Sprintf("Discovery complete. Found %d new nodes", count))
}

// printStats prints current statistics to the console
func (v *Visualizer) printStats() {
	v.statsMutex.RLock()
	defer v.statsMutex.RUnlock()

	// Clear screen (not compatible with all terminals)
	//fmt.Print("\033[H\033[2J")

	// Main title
	greenBold := color.New(color.FgGreen, color.Bold).SprintFunc()
	fmt.Println("")
	fmt.Println(greenBold("=== METEORITE TXN STATS ==="))

	// Transaction stats
	fmt.Printf("Total Txs: %s | Successful: %s | Failed: %s | Success Rate: %s\n",
		color.CyanString("%d", v.txStats.TotalTxs),
		color.GreenString("%d", v.txStats.SuccessfulTxs),
		color.RedString("%d", v.txStats.FailedTxs),
		color.YellowString("%.2f%%", calcSuccessRate(v.txStats)))

	fmt.Printf("TPS: %s | Avg Latency: %s | Latest Block: %s\n",
		color.CyanString("%.2f", v.txStats.TPS),
		color.CyanString("%dms", v.txStats.AverageLatency.Milliseconds()),
		color.CyanString("%d", v.txStats.BlockHeight))

	// Node stats
	fmt.Println("")
	fmt.Println(greenBold("=== NETWORK NODES ==="))

	// Count node states
	online, syncing, offline := 0, 0, 0
	for _, node := range v.nodeStatus {
		if !node.IsOnline {
			offline++
		} else if node.CatchingUp {
			syncing++
		} else {
			online++
		}
	}

	fmt.Printf("Nodes: %s | Online: %s | Syncing: %s | Offline: %s\n",
		color.CyanString("%d", len(v.nodeStatus)),
		color.GreenString("%d", online),
		color.YellowString("%d", syncing),
		color.RedString("%d", offline))

	// Mempool stats
	totalTxs := 0
	totalBytes := int64(0)
	for _, stats := range v.mempoolStats {
		totalTxs += stats.TxCount
		totalBytes += stats.TxBytes
	}

	// Format size nicely
	sizeStr := formatBytes(totalBytes)

	fmt.Printf("Mempool Txs: %s | Size: %s\n",
		color.CyanString("%d", totalTxs),
		color.CyanString("%s", sizeStr))

	// Draw a mini TPS chart using ASCII
	fmt.Println("")
	fmt.Println(greenBold("=== TPS CHART ==="))
	drawAsciiChart(v.chartData)
	fmt.Println("")

	// Log to file
	if v.logFile != nil {
		fmt.Fprintf(v.logFile, "\n=== METEORITE TXN STATS ===\n")
		fmt.Fprintf(v.logFile, "Time: %s\n", time.Now().Format(time.RFC3339))
		fmt.Fprintf(v.logFile, "Total Txs: %d | Successful: %d | Failed: %d | Success Rate: %.2f%%\n",
			v.txStats.TotalTxs, v.txStats.SuccessfulTxs, v.txStats.FailedTxs, calcSuccessRate(v.txStats))
		fmt.Fprintf(v.logFile, "TPS: %.2f | Avg Latency: %dms | Latest Block: %d\n",
			v.txStats.TPS, v.txStats.AverageLatency.Milliseconds(), v.txStats.BlockHeight)
		fmt.Fprintf(v.logFile, "Nodes: %d | Online: %d | Syncing: %d | Offline: %d\n",
			len(v.nodeStatus), online, syncing, offline)
		fmt.Fprintf(v.logFile, "Mempool Txs: %d | Size: %s\n", totalTxs, sizeStr)
	}
}

// calcSuccessRate calculates the success rate percentage
func calcSuccessRate(stats *TransactionStats) float64 {
	if stats.TotalTxs == 0 {
		return 0
	}
	return float64(stats.SuccessfulTxs) / float64(stats.TotalTxs) * 100
}

// formatBytes formats bytes into a human-readable string
func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	} else {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}

// drawAsciiChart draws a simple ASCII chart of TPS data
func drawAsciiChart(data []float64) {
	if len(data) == 0 {
		fmt.Println("No data available yet.")
		return
	}

	// Find max for scaling
	maxVal := float64(0)
	for _, val := range data {
		if val > maxVal {
			maxVal = val
		}
	}

	// Ensure max is at least 1 to avoid division by zero
	if maxVal < 1 {
		maxVal = 1
	}

	// Characters for the chart (from lowest to highest value)
	chars := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

	// Generate lines for each data point
	var line []string
	for _, val := range data {
		// Scale to character range
		idx := int(val / maxVal * float64(len(chars)-1))
		if idx < 0 {
			idx = 0
		} else if idx >= len(chars) {
			idx = len(chars) - 1
		}
		line = append(line, chars[idx])
	}

	// Print the chart with color
	fmt.Print(color.CyanString(strings.Join(line, "")))
	fmt.Printf(" (max: %.1f TPS)\n", maxVal)
}
