package lib

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AddressManager manages addresses loaded from balances.csv
type AddressManager struct {
	addresses     []sdk.AccAddress // Raw account addresses (not bech32 encoded)
	mutex         sync.RWMutex
	initialized   bool
	loadAttempted bool
}

var (
	// singleton instance
	addressManager     *AddressManager
	addressManagerOnce sync.Once
)

// GetAddressManager returns the singleton instance of AddressManager
func GetAddressManager() *AddressManager {
	addressManagerOnce.Do(func() {
		addressManager = &AddressManager{
			addresses:     make([]sdk.AccAddress, 0),
			initialized:   false,
			loadAttempted: false,
		}
	})
	return addressManager
}

// isAddressPrefix checks if a string starts with a known Cosmos chain address prefix
func isAddressPrefix(prefix string) bool {
	knownPrefixes := []string{"cosmos", "osmo", "juno", "sei", "star", "uni"}
	for _, known := range knownPrefixes {
		if prefix == known {
			return true
		}
	}
	return false
}

// processAddressLine attempts to process a line as an address and add it to the address manager
func (am *AddressManager) processAddressLine(line string) bool {
	address := extractAddressFromCSVLine(line)
	if address == "" {
		return false
	}

	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return false
	}

	am.addresses = append(am.addresses, addr)
	return true
}

// extractPrefixFromLine extracts a potential address prefix from a CSV line
func extractPrefixFromLine(line string) string {
	parts := strings.Split(line, ",")
	if len(parts) == 0 {
		return ""
	}

	firstCol := strings.TrimSpace(parts[0])
	prefixParts := strings.Split(firstCol, "1")
	if len(prefixParts) == 0 {
		return ""
	}

	return prefixParts[0]
}

// processFirstLine processes the first line of the CSV file, which might be a header or data
func (am *AddressManager) processFirstLine(scanner *bufio.Scanner) int {
	if !scanner.Scan() {
		// Empty file
		return 0
	}

	line := scanner.Text()
	prefix := extractPrefixFromLine(line)

	// If the line starts with a recognized address prefix, process it as an address
	if isAddressPrefix(prefix) && am.processAddressLine(line) {
		return 1
	}

	// Otherwise assume it's a header and skip it
	return 0
}

// LoadAddressesFromCSV loads addresses from balances.csv file
func (am *AddressManager) LoadAddressesFromCSV() error {
	am.mutex.Lock()
	defer am.mutex.Unlock()

	// If we've already loaded addresses, don't do it again
	if am.initialized {
		return nil
	}

	// If we've already attempted to load and failed, don't try again
	if am.loadAttempted {
		return errors.New("already attempted to load addresses and failed")
	}

	am.loadAttempted = true

	// Try to open balances.csv
	file, err := os.Open("balances.csv")
	if err != nil {
		fmt.Printf("Warning: Could not open balances.csv: %v\n", err)
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	addressCount := 0

	// Process the first line which might be a header
	addressCount += am.processFirstLine(scanner)

	// Process the rest of the lines
	for scanner.Scan() {
		if am.processAddressLine(scanner.Text()) {
			addressCount++
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Warning: Error reading balances.csv: %v\n", err)
		return err
	}

	if addressCount == 0 {
		return errors.New("no valid addresses found")
	}

	fmt.Printf("Loaded %d addresses from balances.csv\n", addressCount)
	am.initialized = true
	return nil
}

// extractAddressFromCSVLine extracts the first column from a CSV line, which should be an address
func extractAddressFromCSVLine(line string) string {
	parts := strings.Split(line, ",")
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return ""
}

// GetRandomAddressWithPrefix returns a random address with the specified bech32 prefix
func (am *AddressManager) GetRandomAddressWithPrefix(prefix string) (string, error) {
	am.mutex.RLock()
	defer am.mutex.RUnlock()

	// If we haven't tried to load addresses yet, do it now
	if !am.initialized && !am.loadAttempted {
		am.mutex.RUnlock() // Unlock for the write operation
		if err := am.LoadAddressesFromCSV(); err != nil {
			am.mutex.RLock() // Lock again for the rest of the function
			fmt.Printf("Failed to load addresses: %v\n", err)
		} else {
			am.mutex.RLock() // Lock again for the rest of the function
		}
	}

	// If we have addresses, pick a random one and convert its prefix
	if len(am.addresses) > 0 {
		// Generate cryptographically secure random number
		randomBytes := make([]byte, 8)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", err
		}

		// Convert to an integer index
		randomInt := int(randomBytes[0]) | int(randomBytes[1])<<8 | int(randomBytes[2])<<16 | int(randomBytes[3])<<24
		idx := randomInt % len(am.addresses)

		// Get the address and convert it to the requested prefix
		addr := am.addresses[idx]
		return sdk.Bech32ifyAddressBytes(prefix, addr)
	}

	// If we don't have addresses, return an error
	return "", errors.New("no addresses available from balances.csv, fallback to random generation")
}

// GenerateDeterministicAccount generates a deterministic account based on the seed string
func GenerateDeterministicAccount(seed string) (sdk.AccAddress, string, error) {
	// Use a hash of the seed to generate bytes
	hashedBytes := []byte(seed)
	for i := 0; i < 3; i++ {
		hashedBytes = hash(hashedBytes)
	}

	// Ensure we have at least 20 bytes
	if len(hashedBytes) < 20 {
		// If we don't have enough bytes, pad with zeros or repeat the hash
		paddedBytes := make([]byte, 20)
		copy(paddedBytes, hashedBytes)
		for i := len(hashedBytes); i < 20; i++ {
			paddedBytes[i] = 0
		}
		hashedBytes = paddedBytes
	}

	// Use the first 20 bytes for the address
	addressBytes := hashedBytes[:20]
	address := sdk.AccAddress(addressBytes)
	bech32Addr, err := sdk.Bech32ifyAddressBytes("cosmos", address)

	return address, bech32Addr, err
}

// GetRandomBytes generates random bytes of the specified length
func GetRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// RandomInt64 generates a random int64 between minVal and maxVal (inclusive)
func RandomInt64(minVal, maxVal int64) int64 {
	if minVal == maxVal {
		return minVal
	}
	if minVal > maxVal {
		minVal, maxVal = maxVal, minVal
	}

	// Generate random bytes
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to current time if crypto/rand fails
		return time.Now().UnixNano()%(maxVal-minVal+1) + minVal
	}

	// Convert to int64 and scale to range
	bigInt := int64(b[0]) | int64(b[1])<<8 | int64(b[2])<<16 | int64(b[3])<<24 |
		int64(b[4])<<32 | int64(b[5])<<40 | int64(b[6])<<48 | int64(b[7])<<56

	// Get absolute value
	if bigInt < 0 {
		bigInt = -bigInt
	}

	return bigInt%(maxVal-minVal+1) + minVal
}

// RandomInt32 generates a random int32 between minVal and maxVal (inclusive)
func RandomInt32(minVal, maxVal int32) int32 {
	if minVal == maxVal {
		return minVal
	}
	if minVal > maxVal {
		minVal, maxVal = maxVal, minVal
	}

	// Generate random bytes
	b := make([]byte, 4)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to current time if crypto/rand fails
		return int32(time.Now().UnixNano())%(maxVal-minVal+1) + minVal
	}

	// Convert to int32 and scale to range
	bigInt := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16 | int32(b[3])<<24

	// Get absolute value
	if bigInt < 0 {
		bigInt = -bigInt
	}

	return bigInt%(maxVal-minVal+1) + minVal
}

// hash is a simple hash function for deterministic account generation
func hash(data []byte) []byte {
	hashBytes := make([]byte, len(data))
	copy(hashBytes, data)

	// Simple hash algorithm for demonstration - in production use a proper hash function
	for i := 0; i < len(hashBytes)-1; i++ {
		hashBytes[i] ^= hashBytes[i+1]
	}
	return hashBytes
}
