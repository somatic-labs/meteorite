package lib

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GenerateDeterministicAccount generates a deterministic account based on a seed string
// This ensures that the same seed always produces the same account
func GenerateDeterministicAccount(seed string) (sdk.AccAddress, error) {
	// Create a HMAC with SHA256
	h := hmac.New(sha256.New, []byte("meteorite-deterministic-account"))

	// Write the seed to the HMAC
	_, err := h.Write([]byte(seed))
	if err != nil {
		return nil, fmt.Errorf("error writing to HMAC: %w", err)
	}

	// Get the result
	result := h.Sum(nil)

	// Use first 20 bytes for the address (standard cosmos address length)
	var addrBytes [20]byte
	copy(addrBytes[:], result[:20])

	// Return the address
	return sdk.AccAddress(addrBytes[:]), nil
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
