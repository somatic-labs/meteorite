package chainregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
)

const (
	// ChainRegistryURL is the URL to the Cosmos Chain Registry
	ChainRegistryURL = "https://github.com/cosmos/chain-registry.git"

	// DefaultCloneDir is the default directory to clone the chain registry to
	DefaultCloneDir = ".chain-registry"

	// ChainFilePattern is the pattern for chain JSON files
	ChainFilePattern = "chain.json"

	// DefaultTimeout for HTTP requests
	DefaultTimeout = 10 * time.Second
)

// Chain represents a chain in the registry
type Chain struct {
	ChainName    string   `json:"chain_name"`
	Status       string   `json:"status"`
	NetworkType  string   `json:"network_type"`
	PrettyName   string   `json:"pretty_name"`
	ChainID      string   `json:"chain_id"`
	Bech32Prefix string   `json:"bech32_prefix"`
	DaemonName   string   `json:"daemon_name"`
	NodeHome     string   `json:"node_home"`
	Genesis      Genesis  `json:"genesis"`
	Codebase     Codebase `json:"codebase"`
	Peers        Peers    `json:"peers"`
	APIs         APIs     `json:"apis"`
	Fees         Fees     `json:"fees"`
	Staking      Staking  `json:"staking"`
	Description  string   `json:"description,omitempty"`
}

// Genesis contains genesis related information
type Genesis struct {
	GenesisURL string `json:"genesis_url"`
}

// Codebase contains information about the chain's codebase
type Codebase struct {
	GitRepo            string   `json:"git_repo"`
	RecommendedVersion string   `json:"recommended_version"`
	CompatibleVersions []string `json:"compatible_versions"`
}

// Peers contains information about peers
type Peers struct {
	Seeds           []PeerInfo `json:"seeds"`
	PersistentPeers []PeerInfo `json:"persistent_peers"`
}

// PeerInfo contains information about a peer
type PeerInfo struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Provider string `json:"provider,omitempty"`
}

// APIs contains information about API endpoints
type APIs struct {
	RPC     []APIEndpoint `json:"rpc"`
	Rest    []APIEndpoint `json:"rest"`
	GRPC    []APIEndpoint `json:"grpc"`
	API     []APIEndpoint `json:"api,omitempty"`
	Archive []APIEndpoint `json:"archive,omitempty"`
}

// APIEndpoint contains information about an API endpoint
type APIEndpoint struct {
	Address  string `json:"address"`
	Provider string `json:"provider,omitempty"`
}

// Fees contains information about fees
type Fees struct {
	FeeTokens []FeeToken `json:"fee_tokens"`
}

// FeeToken contains information about a fee token
type FeeToken struct {
	Denom            string  `json:"denom"`
	FixedMinGasPrice float64 `json:"fixed_min_gas_price"`
}

// Staking contains information about staking
type Staking struct {
	StakingTokens []StakingToken `json:"staking_tokens"`
}

// StakingToken contains information about a staking token
type StakingToken struct {
	Denom string `json:"denom"`
}

// Registry handles operations with the chain registry
type Registry struct {
	repoPath string
	chains   map[string]*Chain
	mutex    sync.RWMutex
}

// NewRegistry creates a new Registry
func NewRegistry(repoPath string) *Registry {
	if repoPath == "" {
		homeDir, _ := os.UserHomeDir()
		repoPath = filepath.Join(homeDir, DefaultCloneDir)
	}
	return &Registry{
		repoPath: repoPath,
		chains:   make(map[string]*Chain),
	}
}

// cloneRegistry clones the chain registry repository
func (r *Registry) cloneRegistry() error {
	fmt.Printf("Cloning chain registry to %s...\n", r.repoPath)
	_, err := git.PlainClone(r.repoPath, false, &git.CloneOptions{
		URL:      ChainRegistryURL,
		Progress: os.Stdout,
	})
	if err != nil {
		return fmt.Errorf("error cloning repository: %w", err)
	}
	return nil
}

// updateRegistry pulls the latest changes to the registry
func (r *Registry) updateRegistry() error {
	fmt.Printf("Pulling latest changes to %s...\n", r.repoPath)
	repo, err := git.PlainOpen(r.repoPath)
	if err != nil {
		return fmt.Errorf("error opening repository: %w", err)
	}

	// Get worktree
	w, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("error getting worktree: %w", err)
	}

	// Pull
	err = w.Pull(&git.PullOptions{
		RemoteName: "origin",
		Progress:   os.Stdout,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("error pulling repository: %w", err)
	}

	return nil
}

// Download downloads the chain registry
func (r *Registry) Download() error {
	// Check if directory exists
	_, err := os.Stat(r.repoPath)
	if os.IsNotExist(err) {
		// Clone the repository if it doesn't exist
		return r.cloneRegistry()
	}

	// Directory exists, update the registry
	return r.updateRegistry()
}

// LoadChains loads all chains from the registry
func (r *Registry) LoadChains() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Walk through the repository directory
	err := filepath.Walk(r.repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Skip files that are not chain.json
		if filepath.Base(path) != ChainFilePattern {
			return nil
		}

		// Get chain directory name (which is the chain name)
		chainName := filepath.Base(filepath.Dir(path))

		// Skip _template directory
		if chainName == "_template" {
			return nil
		}

		// Read chain file
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("Error reading %s: %v\n", path, err)
			return nil // Continue with other chains
		}

		// Parse chain file
		var chain Chain
		err = json.Unmarshal(data, &chain)
		if err != nil {
			fmt.Printf("Error parsing %s: %v\n", path, err)
			return nil // Continue with other chains
		}

		// Store chain
		r.chains[chainName] = &chain

		return nil
	})
	if err != nil {
		return fmt.Errorf("error walking repository directory: %w", err)
	}

	fmt.Printf("Loaded %d chains from registry\n", len(r.chains))
	return nil
}

// GetChains returns all chains in the registry
func (r *Registry) GetChains() []*Chain {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	chains := make([]*Chain, 0, len(r.chains))
	for _, chain := range r.chains {
		chains = append(chains, chain)
	}

	// Sort chains by name for consistent output
	sort.Slice(chains, func(i, j int) bool {
		return chains[i].ChainName < chains[j].ChainName
	})

	return chains
}

// GetChain returns a specific chain by name
func (r *Registry) GetChain(name string) *Chain {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return r.chains[name]
}

// TestRPCEndpoint tests if an RPC endpoint is accessible
func TestRPCEndpoint(endpoint string) (bool, error) {
	// Ensure endpoint has a protocol
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "http://" + endpoint
	}

	// Ensure endpoint has the correct path
	if !strings.HasSuffix(endpoint, "/") {
		endpoint += "/"
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: DefaultTimeout,
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"status", nil)
	if err != nil {
		return false, fmt.Errorf("error creating request: %w", err)
	}

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return false, nil // Endpoint is not accessible, but not an error
	}
	defer resp.Body.Close()

	// Read response body
	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return false, nil // Endpoint is not accessible, but not an error
	}

	// Check response status code
	if resp.StatusCode != http.StatusOK {
		return false, nil // Endpoint is not accessible, but not an error
	}

	return true, nil // Endpoint is accessible
}

// FindOpenRPCEndpoints finds all open RPC endpoints for a chain
func (r *Registry) FindOpenRPCEndpoints(chainName string) ([]string, error) {
	chain := r.GetChain(chainName)
	if chain == nil {
		return nil, fmt.Errorf("chain %s not found", chainName)
	}

	var openEndpoints []string
	var wg sync.WaitGroup
	var mutex sync.Mutex

	fmt.Printf("Testing %d RPC endpoints for %s...\n", len(chain.APIs.RPC), chain.ChainName)

	for _, rpc := range chain.APIs.RPC {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()

			fmt.Printf("Testing endpoint: %s\n", endpoint)
			accessible, _ := TestRPCEndpoint(endpoint)

			if accessible {
				mutex.Lock()
				openEndpoints = append(openEndpoints, endpoint)
				mutex.Unlock()
				fmt.Printf("Endpoint %s is accessible\n", endpoint)
			} else {
				fmt.Printf("Endpoint %s is not accessible\n", endpoint)
			}
		}(rpc.Address)
	}

	wg.Wait()

	if len(openEndpoints) == 0 {
		return nil, fmt.Errorf("no open RPC endpoints found for %s", chainName)
	}

	return openEndpoints, nil
}
