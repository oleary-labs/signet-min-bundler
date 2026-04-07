package bundler

import (
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum"
)

// isNotFound returns true if the error indicates a transaction receipt was not
// found — i.e. the tx is pending or unknown. This is expected during normal
// polling and should not be logged as a warning.
func isNotFound(err error) bool {
	if errors.Is(err, ethereum.NotFound) {
		return true
	}
	// Some RPC providers return non-standard error messages.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found")
}
