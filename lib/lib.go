package lib

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	types "github.com/somatic-labs/meteorite/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second, // Adjusted timeout to 30 seconds
	Transport: &http.Transport{
		MaxIdleConns:        100,              // Increased maximum idle connections
		MaxIdleConnsPerHost: 10,               // Increased maximum idle connections per host
		IdleConnTimeout:     90 * time.Second, // Increased idle connection timeout
		TLSHandshakeTimeout: 10 * time.Second, // Increased TLS handshake timeout
	},
}

// BalanceCache holds cached account balances with expiration times
type BalanceCache struct {
	cache map[string]balanceCacheEntry
	mutex sync.RWMutex
}

type balanceCacheEntry struct {
	balance    sdkmath.Int
	expiration time.Time
}

// Default cache expiration time (10 seconds is a reasonable default for balance queries)
const balanceCacheTTL = 10 * time.Second

// Global balance cache instance
var (
	globalBalanceCache *BalanceCache
	balanceCacheOnce   sync.Once
)

// GetBalanceCache returns the global balance cache instance
func GetBalanceCache() *BalanceCache {
	balanceCacheOnce.Do(func() {
		globalBalanceCache = &BalanceCache{
			cache: make(map[string]balanceCacheEntry),
		}
	})
	return globalBalanceCache
}

// Get retrieves a cached balance if available and not expired
func (bc *BalanceCache) Get(key string) (sdkmath.Int, bool) {
	bc.mutex.RLock()
	defer bc.mutex.RUnlock()

	entry, found := bc.cache[key]
	if !found {
		return sdkmath.ZeroInt(), false
	}

	// Check if the cache entry has expired
	if time.Now().After(entry.expiration) {
		return sdkmath.ZeroInt(), false
	}

	return entry.balance, true
}

// Set stores a balance in the cache with an expiration time
func (bc *BalanceCache) Set(key string, balance sdkmath.Int, ttl time.Duration) {
	bc.mutex.Lock()
	defer bc.mutex.Unlock()

	bc.cache[key] = balanceCacheEntry{
		balance:    balance,
		expiration: time.Now().Add(ttl),
	}
}

// Invalidate removes a specific key from the cache
func (bc *BalanceCache) Invalidate(key string) {
	bc.mutex.Lock()
	defer bc.mutex.Unlock()

	delete(bc.cache, key)
}

// Clear empties the entire cache
func (bc *BalanceCache) Clear() {
	bc.mutex.Lock()
	defer bc.mutex.Unlock()

	bc.cache = make(map[string]balanceCacheEntry)
}

func GetAccountInfo(address string, config types.Config) (seqint, accnum uint64, err error) {
	resp, err := HTTPGet(config.Nodes.API + "/cosmos/auth/v1beta1/accounts/" + address)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get initial sequence: %v", err)
	}

	var accountRes types.AccountResult
	err = json.Unmarshal(resp, &accountRes)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal account result: %v", err)
	}

	seqint, err = strconv.ParseUint(accountRes.Account.Sequence, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert sequence to int: %v", err)
	}

	accnum, err = strconv.ParseUint(accountRes.Account.AccountNumber, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert account number to int: %v", err)
	}

	return seqint, accnum, nil
}

func GetChainID(nodeURL string) (string, error) {
	resp, err := HTTPGet(nodeURL + "/status")
	if err != nil {
		log.Printf("Failed to get node status: %v", err)
		return "", err
	}

	var statusRes types.NodeStatusResponse
	err = json.Unmarshal(resp, &statusRes)
	if err != nil {
		log.Printf("Failed to unmarshal node status result: %v", err)
		return "", err
	}

	return statusRes.Result.NodeInfo.Network, nil
}

func HTTPGet(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("Sending HTTP GET request to: %s", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("Error creating HTTP request for %s: %v", url, err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers to appear as a normal client
	req.Header.Set("User-Agent", "meteorite/1.0")
	req.Header.Set("Accept", "application/json")

	// Track request time for debugging
	startTime := time.Now()
	resp, err := httpClient.Do(req)
	requestDuration := time.Since(startTime)

	if err != nil {
		netErr, ok := err.(net.Error)
		if ok && netErr.Timeout() {
			log.Printf("Request to %s timed out after %v", url, requestDuration)
			return nil, fmt.Errorf("request timeout after %v: %w", requestDuration, err)
		}
		log.Printf("HTTP request to %s failed after %v: %v", url, requestDuration, err)
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check for non-success status codes
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		bodyPreview := string(body)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "... [truncated]"
		}
		log.Printf("HTTP request to %s returned status %d in %v: %s",
			url, resp.StatusCode, requestDuration, bodyPreview)
		return nil, fmt.Errorf("HTTP status %d (%v): %s", resp.StatusCode, requestDuration, bodyPreview)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body from %s after %v: %v", url, requestDuration, err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for empty responses
	if len(body) == 0 {
		log.Printf("Warning: Empty response body from %s after %v", url, requestDuration)
		return nil, errors.New("empty response body")
	}

	// Debug: Log content type, response size and timing
	log.Printf("Response from %s: Content-Type: %s, Size: %d bytes, Time: %v",
		url, resp.Header.Get("Content-Type"), len(body), requestDuration)

	// For debugging, print a preview of the response
	bodyPreview := string(body)
	if len(bodyPreview) > 300 {
		bodyPreview = bodyPreview[:300] + "... [truncated]"
	}
	log.Printf("Response preview: %s", bodyPreview)

	return body, nil
}

// This function will load our nodes from nodes.toml.
func LoadNodes() []string {
	var config types.Config
	if _, err := toml.DecodeFile("nodes.toml", &config); err != nil {
		log.Fatalf("Failed to load nodes.toml: %v", err)
	}
	return config.Nodes.RPC
}

func GenerateRandomString(config types.Config) (string, error) {
	// Generate a random size between config.RandMin and config.RandMax
	sizeB, err := rand.Int(rand.Reader, big.NewInt(config.RandMax-config.RandMin+1))
	if err != nil {
		return "", err
	}
	sizeB = sizeB.Add(sizeB, big.NewInt(config.RandMin))

	// Calculate the number of bytes to generate (2 characters per byte in hex encoding)
	nBytes := int(sizeB.Int64()) / 2

	randomBytes := make([]byte, nBytes)
	_, err = rand.Read(randomBytes)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(randomBytes), nil
}

func GenerateRandomStringOfLength(n int) (string, error) {
	letters := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	b := make([]rune, n)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		b[i] = letters[num.Int64()]
	}
	return string(b), nil
}

func GenerateRandomAccount() (sdk.AccAddress, error) {
	// Generate 20 random bytes
	randomBytes := make([]byte, 20)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return nil, err
	}

	// Create an AccAddress from the random bytes
	accAddress := sdk.AccAddress(randomBytes)

	return accAddress, nil
}

func GetBalances(accounts []types.Account, config types.Config) (map[string]sdkmath.Int, error) {
	balances := make(map[string]sdkmath.Int)

	// Create a worker pool for parallel balance fetching
	const maxWorkers = 15 // Limit concurrent requests

	// Create channels for work distribution and result collection
	type balanceResult struct {
		address string
		balance sdkmath.Int
		err     error
	}

	// If we have fewer accounts than max workers, adjust the worker count
	workerCount := maxWorkers
	if len(accounts) < maxWorkers {
		workerCount = len(accounts)
	}

	// No accounts? Return early
	if len(accounts) == 0 {
		return balances, nil
	}

	log.Printf("Fetching balances for %d accounts using %d workers", len(accounts), workerCount)

	// Create a worker pool
	jobs := make(chan string, len(accounts))
	results := make(chan balanceResult, len(accounts))

	// Error tracking
	var errs []error
	var errMutex sync.Mutex

	// Start workers
	var wg sync.WaitGroup
	for w := 1; w <= workerCount; w++ {
		wg.Add(1)
		go func(workerId int) {
			defer wg.Done()

			for address := range jobs {
				balance, err := GetAccountBalance(address, config)
				results <- balanceResult{
					address: address,
					balance: balance,
					err:     err,
				}

				if err != nil {
					errMutex.Lock()
					errs = append(errs, fmt.Errorf("worker %d: %s - %w", workerId, address, err))
					errMutex.Unlock()
				}
			}
		}(w)
	}

	// Send jobs
	for _, account := range accounts {
		jobs <- account.Address
	}
	close(jobs)

	// Wait for all workers to complete and close results channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Process results
	for result := range results {
		if result.err == nil {
			balances[result.address] = result.balance
		}
	}

	// If we encountered errors but still got some balances
	if len(errs) > 0 && len(balances) > 0 {
		log.Printf("Warning: Encountered %d errors while fetching balances, but got %d valid balances",
			len(errs), len(balances))
		// Return the partial results if we have some balances
		return balances, nil
	}

	// If we have errors and no balances, return the first error
	if len(errs) > 0 {
		return nil, fmt.Errorf("failed to fetch any balances: %w", errs[0])
	}

	return balances, nil
}

// GetAccountBalance fetches the account balance for a given address and denomination with caching support
func GetAccountBalance(address string, config types.Config) (sdkmath.Int, error) {
	// Create cache key from address and denom to support multiple chains/denoms
	cacheKey := fmt.Sprintf("%s:%s", address, config.Denom)

	// Try to get from cache first
	cache := GetBalanceCache()
	if balance, found := cache.Get(cacheKey); found {
		log.Printf("Using cached balance for %s", address)
		return balance, nil
	}

	// Ensure the API URL doesn't end with a trailing slash
	apiBase := strings.TrimSuffix(config.Nodes.API, "/")
	apiURL := apiBase + "/cosmos/bank/v1beta1/balances/" + address
	log.Printf("Fetching balance from API: %s", apiURL)

	// Create a context with timeout for the HTTP request
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return sdkmath.ZeroInt(), fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers for better compatibility
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Meteorite/1.0")

	// Use a shared HTTP client with connection pooling
	client := GetSharedHTTPClient()

	// Make the HTTP request with timing
	startTime := time.Now()
	resp, err := client.Do(req)
	requestDuration := time.Since(startTime)

	if err != nil {
		return sdkmath.ZeroInt(), fmt.Errorf("failed to get balance from API: %w", err)
	}
	defer resp.Body.Close()

	// Check HTTP status code
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return sdkmath.ZeroInt(), fmt.Errorf("API returned non-200 status code: %d, body: %s",
			resp.StatusCode, truncateString(string(bodyBytes), 200))
	}

	// Read response body with limited size (prevent memory issues)
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // Limit to 1MB
	if err != nil {
		return sdkmath.ZeroInt(), fmt.Errorf("error reading response body: %w", err)
	}

	log.Printf("Balance API request completed in %s", requestDuration)

	// If response is empty or nil, return early with a clear error
	if len(bodyBytes) == 0 {
		return sdkmath.ZeroInt(), fmt.Errorf("empty response from API for address %s", address)
	}

	// Debug: Log the raw response (limit to 200 characters to avoid log spam)
	respStr := string(bodyBytes)
	if len(respStr) > 200 {
		log.Printf("Raw API response for %s (truncated): %s...", address, respStr[:200])
	} else {
		log.Printf("Raw API response for %s: %s", address, respStr)
	}

	// Try to detect if we received non-JSON response (like HTML error page)
	contentType := detectContentType(bodyBytes)
	if contentType != "application/json" && contentType != "unknown" {
		log.Printf("Warning: Received non-JSON response (%s) from API for address %s", contentType, address)
		return sdkmath.ZeroInt(), fmt.Errorf("non-JSON response from API (content type: %s)", contentType)
	}

	// Verify that the response is valid JSON
	if !json.Valid(bodyBytes) {
		previewLength := 100
		if len(respStr) < previewLength {
			previewLength = len(respStr)
		}
		log.Printf("Invalid JSON response from API. Preview: %s", respStr[:previewLength])
		return sdkmath.ZeroInt(), fmt.Errorf("invalid JSON response from API for address %s", address)
	}

	// Try to unmarshal the response into our balance structure
	var balanceRes types.BalanceResult
	err = json.Unmarshal(bodyBytes, &balanceRes)
	if err != nil {
		// Try alternative formats in case the API response structure is different
		// Some APIs might return a different structure or wrap the balance differently
		altParsed, altErr := parseAlternativeBalanceFormat(bodyBytes, config.Denom)
		if altErr == nil {
			log.Printf("Successfully parsed balance using alternative format for %s", address)
			// Cache the result
			cache.Set(cacheKey, altParsed, balanceCacheTTL)
			return altParsed, nil
		}

		log.Printf("Failed to unmarshal balance response: %v", err)
		return sdkmath.ZeroInt(), fmt.Errorf("failed to unmarshal balance response: %w", err)
	}

	// Verify that the required fields exist
	if balanceRes.Balances == nil {
		// Try a last-ditch effort to find any balance information
		log.Printf("Balance response missing 'balances' field for address %s", address)
		altParsed, altErr := parseAlternativeBalanceFormat(bodyBytes, config.Denom)
		if altErr == nil {
			log.Printf("Found balance using alternative format for %s", address)
			// Cache the result
			cache.Set(cacheKey, altParsed, balanceCacheTTL)
			return altParsed, nil
		}
		return sdkmath.ZeroInt(), fmt.Errorf("balance response missing 'balances' field for address %s", address)
	}

	for _, coin := range balanceRes.Balances {
		if coin.Denom == config.Denom {
			amount, ok := sdkmath.NewIntFromString(coin.Amount)
			if !ok {
				return sdkmath.ZeroInt(), fmt.Errorf("invalid coin amount '%s' for denom %s", coin.Amount, coin.Denom)
			}
			log.Printf("Found balance for %s: %s %s", address, amount.String(), coin.Denom)
			// Cache the successful result
			cache.Set(cacheKey, amount, balanceCacheTTL)
			return amount, nil
		}
	}

	// If no balance found for the denom, return zero balance
	log.Printf("Denomination %s not found in account balances for %s", config.Denom, address)
	// Cache the zero balance too
	cache.Set(cacheKey, sdkmath.ZeroInt(), balanceCacheTTL)
	return sdkmath.ZeroInt(), nil // Return zero without error since this is a valid state
}

// truncateString truncates a string to the specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// GetSharedHTTPClient returns a shared HTTP client with connection pooling
var (
	sharedHTTPClient *http.Client
	httpClientOnce   sync.Once
)

func GetSharedHTTPClient() *http.Client {
	httpClientOnce.Do(func() {
		transport := &http.Transport{
			MaxIdleConns:        100,              // Maximum number of idle connections
			MaxIdleConnsPerHost: 10,               // Maximum idle connections per host
			IdleConnTimeout:     90 * time.Second, // How long connections can remain idle
			DisableCompression:  false,            // Enable compression
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,            // Don't skip TLS verification
				MinVersion:         tls.VersionTLS12, // Minimum TLS version 1.2
			},
		}

		sharedHTTPClient = &http.Client{
			Timeout:   30 * time.Second, // Overall timeout for requests
			Transport: transport,
		}
	})
	return sharedHTTPClient
}

// detectContentType tries to determine what kind of content we received
func detectContentType(data []byte) string {
	// Check for common content type signatures
	if len(data) > 5 && data[0] == '<' {
		if bytes.Contains(data, []byte("<html")) || bytes.Contains(data, []byte("<HTML")) {
			return "text/html"
		}
		if bytes.Contains(data, []byte("<?xml")) || bytes.Contains(data, []byte("<xml")) {
			return "text/xml"
		}
		return "text/plain"
	}

	// Check if it's likely JSON
	if len(data) > 1 && (data[0] == '{' || data[0] == '[') {
		return "application/json"
	}

	return "unknown"
}

// parseAlternativeBalanceFormat attempts to extract balance information from non-standard API responses
func parseAlternativeBalanceFormat(data []byte, targetDenom string) (sdkmath.Int, error) {
	// This is a fallback method to try to extract balance information from different API response formats

	// Try to unmarshal into a generic map to handle various structure types
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return sdkmath.ZeroInt(), fmt.Errorf("failed to parse alternative format: %w", err)
	}

	// Look for common patterns in various chain API responses
	// Try to navigate through nested structures to find balance information

	// Try each pattern in sequence
	if balance, found := tryCoinsPattern(result, targetDenom); found {
		return balance, nil
	}

	if balance, found := tryBalancePattern(result, targetDenom); found {
		return balance, nil
	}

	if balance, found := tryBalancesArrayPattern(result, targetDenom); found {
		return balance, nil
	}

	return sdkmath.ZeroInt(), errors.New("could not find balance in alternative format")
}

// tryCoinsPattern tries to extract balance from result.coins[] structure
func tryCoinsPattern(result map[string]interface{}, targetDenom string) (sdkmath.Int, bool) {
	coins, ok := getNestedArray(result, "result", "coins")
	if !ok {
		return sdkmath.ZeroInt(), false
	}

	for _, coin := range coins {
		coinMap, ok := coin.(map[string]interface{})
		if !ok {
			continue
		}

		denom, ok := coinMap["denom"].(string)
		if !ok || denom != targetDenom {
			continue
		}

		amount, ok := coinMap["amount"].(string)
		if !ok {
			continue
		}

		parsedAmount, ok := sdkmath.NewIntFromString(amount)
		if !ok {
			continue
		}

		return parsedAmount, true
	}

	return sdkmath.ZeroInt(), false
}

// tryBalancePattern tries to extract balance from result.balance structure
func tryBalancePattern(result map[string]interface{}, targetDenom string) (sdkmath.Int, bool) {
	balance, ok := getNestedMap(result, "result", "balance")
	if !ok {
		return sdkmath.ZeroInt(), false
	}

	denom, ok := balance["denom"].(string)
	if !ok || denom != targetDenom {
		return sdkmath.ZeroInt(), false
	}

	amount, ok := balance["amount"].(string)
	if !ok {
		return sdkmath.ZeroInt(), false
	}

	parsedAmount, ok := sdkmath.NewIntFromString(amount)
	if !ok {
		return sdkmath.ZeroInt(), false
	}

	return parsedAmount, true
}

// tryBalancesArrayPattern tries to extract balance from balances array
func tryBalancesArrayPattern(result map[string]interface{}, targetDenom string) (sdkmath.Int, bool) {
	balances, ok := result["balances"].([]interface{})
	if !ok {
		return sdkmath.ZeroInt(), false
	}

	for _, bal := range balances {
		balMap, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}

		denom, ok := balMap["denom"].(string)
		if !ok || denom != targetDenom {
			continue
		}

		amount, ok := balMap["amount"].(string)
		if !ok {
			continue
		}

		parsedAmount, ok := sdkmath.NewIntFromString(amount)
		if !ok {
			continue
		}

		return parsedAmount, true
	}

	return sdkmath.ZeroInt(), false
}

// Helper function to safely navigate nested maps
func getNestedMap(data map[string]interface{}, keys ...string) (map[string]interface{}, bool) {
	current := data
	for i, key := range keys {
		if i == len(keys)-1 {
			if val, ok := current[key].(map[string]interface{}); ok {
				return val, true
			}
			return nil, false
		}

		if next, ok := current[key].(map[string]interface{}); ok {
			current = next
		} else {
			return nil, false
		}
	}
	return nil, false
}

// Helper function to safely navigate to an array in nested maps
func getNestedArray(data map[string]interface{}, keys ...string) ([]interface{}, bool) {
	current := data
	for i, key := range keys {
		if i == len(keys)-1 {
			if val, ok := current[key].([]interface{}); ok {
				return val, true
			}
			return nil, false
		}

		if next, ok := current[key].(map[string]interface{}); ok {
			current = next
		} else {
			return nil, false
		}
	}
	return nil, false
}

func CheckBalancesWithinThreshold(balances map[string]sdkmath.Int, threshold float64) bool {
	// Early return if no balances
	if len(balances) == 0 {
		return false
	}

	// We'll track valid balances to ensure we have at least one
	validBalanceCount := 0
	var minBalance, maxBalance sdkmath.Int
	first := true

	for _, balance := range balances {
		// Skip nil balances
		if balance.IsNil() {
			continue
		}

		validBalanceCount++

		if first {
			minBalance = balance
			maxBalance = balance
			first = false
			continue
		}

		if balance.LT(minBalance) {
			minBalance = balance
		}
		if balance.GT(maxBalance) {
			maxBalance = balance
		}
	}

	// If we didn't find any valid balances, return false
	if validBalanceCount == 0 {
		return false
	}

	// Skip check if all balances are below minimum threshold
	minThreshold := sdkmath.NewInt(1000000) // 1 token assuming 6 decimals
	if maxBalance.LT(minThreshold) {
		return true
	}

	// Calculate the difference as a percentage of the max balance
	if maxBalance.IsZero() {
		return minBalance.IsZero()
	}

	diff := maxBalance.Sub(minBalance)
	diffFloat := float64(diff.Int64())
	maxFloat := float64(maxBalance.Int64())

	percentage := diffFloat / maxFloat
	return percentage <= threshold
}

// Function to extract the expected sequence number from the error message
func ExtractExpectedSequence(errMsg string) (uint64, error) {
	// Parse the error message to extract the expected sequence number
	// Example error message:
	// "account sequence mismatch, expected 42, got 41: incorrect account sequence"
	if !strings.Contains(errMsg, "account sequence mismatch") {
		return 0, fmt.Errorf("unexpected error message format: %s", errMsg)
	}

	index := strings.Index(errMsg, "expected ")
	if index == -1 {
		return 0, errors.New("expected sequence not found in error message")
	}

	start := index + len("expected ")
	rest := errMsg[start:]
	parts := strings.SplitN(rest, ",", 2)
	if len(parts) < 1 {
		return 0, errors.New("failed to split expected sequence from error message")
	}

	expectedSeqStr := strings.TrimSpace(parts[0])
	expectedSeq, err := strconv.ParseUint(expectedSeqStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse expected sequence number: %v", err)
	}

	return expectedSeq, nil
}

// ExtractRequiredFee parses an error message to extract the required fee amount and denom
// Example: "insufficient fees; got: 9015uatone required: 90150uatone: insufficient fee"
func ExtractRequiredFee(errMsg string) (amount int64, denom string, err error) {
	// Try to extract using regex that matches the standard format
	re := regexp.MustCompile(`required: (\d+)([a-zA-Z]+)`)
	matches := re.FindStringSubmatch(errMsg)

	if len(matches) == 3 {
		// Parse the amount
		amount, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("failed to parse required fee amount: %w", err)
		}

		// Get the denom
		denom := matches[2]
		return amount, denom, nil
	}

	return 0, "", fmt.Errorf("could not extract required fee from error message: %s", errMsg)
}
