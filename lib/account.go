package lib

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AddressManager handles loading and managing addresses from the balances.csv file
type AddressManager struct {
	addresses     []sdk.AccAddress // Raw account addresses (not bech32 encoded)
	mutex         sync.RWMutex
	initialized   bool
	loadAttempted bool
}

var (
	// Global singleton instance of AddressManager
	globalAddressManager = &AddressManager{}
	addressOnce          sync.Once
)

// GetAddressManager returns the singleton instance of AddressManager
func GetAddressManager() *AddressManager {
	addressOnce.Do(func() {
		globalAddressManager.initialized = false
		globalAddressManager.loadAttempted = false
		globalAddressManager.addresses = []sdk.AccAddress{}
		// Initialize random seed
		mrand.Seed(time.Now().UnixNano())
	})
	return globalAddressManager
}

// LoadAddressesFromCSV loads addresses from the balances.csv file
// This is a best-effort operation - if the file doesn't exist or is invalid,
// we'll log an error and fallback to random address generation
func (am *AddressManager) LoadAddressesFromCSV() error {
	am.mutex.Lock()
	defer am.mutex.Unlock()

	// Mark that we've attempted to load
	am.loadAttempted = true

	// Check if balances.csv exists
	file, err := os.Open("balances.csv")
	if err != nil {
		fmt.Printf("Warning: balances.csv not found or couldn't be opened: %v\n", err)
		fmt.Println("Will use random address generation as fallback")
		return err
	}
	defer file.Close()

	// Read addresses from the CSV file
	scanner := bufio.NewScanner(file)
	var addresses []sdk.AccAddress

	// Skip header line if it exists
	if scanner.Scan() {
		line := scanner.Text()
		// If the first line doesn't look like an address, assume it's a header
		if !strings.HasPrefix(line, "cosmos") && !strings.HasPrefix(line, "osmo") &&
			!strings.HasPrefix(line, "juno") && !strings.HasPrefix(line, "akash") &&
			!strings.HasPrefix(line, "star") && !strings.HasPrefix(line, "uni") {
			// It's a header, continue to the next line
		} else {
			// It looks like an address, process it
			address := extractAddressFromCSVLine(line)
			if address != "" {
				addr, err := sdk.AccAddressFromBech32(address)
				if err == nil {
					addresses = append(addresses, addr)
				}
			}
		}
	}

	// Process the rest of the file
	for scanner.Scan() {
		line := scanner.Text()
		address := extractAddressFromCSVLine(line)
		if address != "" {
			addr, err := sdk.AccAddressFromBech32(address)
			if err == nil {
				addresses = append(addresses, addr)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Error reading balances.csv: %v\n", err)
		return err
	}

	if len(addresses) == 0 {
		fmt.Println("Warning: No valid addresses found in balances.csv")
		return fmt.Errorf("no valid addresses found")
	}

	fmt.Printf("Successfully loaded %d addresses from balances.csv\n", len(addresses))
	am.addresses = addresses
	am.initialized = true
	return nil
}

// extractAddressFromCSVLine extracts the address from a CSV line
// Format is expected to be: address,balance1,balance2,...
func extractAddressFromCSVLine(line string) string {
	parts := strings.Split(line, ",")
	if len(parts) == 0 {
		return ""
	}

	// The address should be the first column
	address := strings.TrimSpace(parts[0])
	return address
}

// GetRandomAddressWithPrefix returns a random address with the specified bech32 prefix
// If addresses are loaded from balances.csv, it will pick a random one and convert the prefix
// Otherwise, it will fall back to the regular random account generation in lib/lib.go
func (am *AddressManager) GetRandomAddressWithPrefix(prefix string) (string, error) {
	am.mutex.RLock()

	// If we haven't tried to load addresses yet, attempt to do so
	if !am.loadAttempted {
		am.mutex.RUnlock() // Unlock for reading before locking for writing
		am.LoadAddressesFromCSV()
		am.mutex.RLock() // Lock for reading again after loading
	}

	// If we have addresses, use one of them
	if am.initialized && len(am.addresses) > 0 {
		// Get a random address from our list
		idx := mrand.Intn(len(am.addresses))
		addr := am.addresses[idx]
		am.mutex.RUnlock()

		// Convert to the requested prefix
		bech32Addr, err := sdk.Bech32ifyAddressBytes(prefix, addr)
		if err != nil {
			return "", fmt.Errorf("failed to convert address to bech32: %w", err)
		}
		return bech32Addr, nil
	}
	am.mutex.RUnlock()

	// If we don't have addresses, fallback to lib.GenerateRandomAccount
	// We don't call it directly here to avoid circular imports
	// The caller should handle this case by using lib.GenerateRandomAccount
	return "", fmt.Errorf("no addresses available from balances.csv, fallback to random generation")
}

// GenerateDeterministicAccount generates a deterministic account based on a seed string
// This ensures that the same seed always produces the same account
func GenerateDeterministicAccount(seed string) (sdk.AccAddress, string, error) {
	// Hash the seed
	h := hmac.New(sha256.New, []byte("meteorite-deterministic-account"))
	h.Write([]byte(seed))
	hash := h.Sum(nil)

	// Use the first 20 bytes as the address
	addrBytes := hash[:20]

	bech32Addr := sdk.AccAddress(addrBytes).String()

	return sdk.AccAddress(addrBytes), bech32Addr, nil
}

// GetRandomBytes generates n random bytes
func GetRandomBytes(n int) ([]byte, error) {
	bytes := make([]byte, n)
	_, err := rand.Read(bytes)
	if err != nil {
		return nil, err
	}
	return bytes, nil
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
	bytes, _ := GetRandomBytes(8)
	// Convert to int64 (use modulo to stay within range)
	r := int64(binary.LittleEndian.Uint64(bytes))
	if r < 0 {
		r = -r
	}
	// Adjust to range
	return minVal + (r % (maxVal - minVal + 1))
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
	bytes, _ := GetRandomBytes(4)
	// Convert to int32 (use modulo to stay within range)
	r := int32(binary.LittleEndian.Uint32(bytes))
	if r < 0 {
		r = -r
	}
	// Adjust to range
	return minVal + (r % (maxVal - minVal + 1))
}
