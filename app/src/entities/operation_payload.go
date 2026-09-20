package entities

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// OperationPayload is the business content of an external operation, and nothing else. It is
// what the idempotency hash is computed over, so it deliberately leaves out everything that is
// transport: the idempotency key, the HTTP headers, the queue envelope and the timestamps.
//
// The fields are declared in alphabetical order of their JSON names. encoding/json writes a
// struct in declaration order, so this is what makes the serialisation canonical: keys sorted,
// no whitespace, and the same bytes for the same content whichever entry point delivered it.
type OperationPayload struct {
	ExternalTransactionID          string          `json:"externalTransactionId"`
	GameID                         string          `json:"gameId"`
	Kind                           TransactionKind `json:"kind"`
	Money                          Money           `json:"money"`
	PlayerID                       string          `json:"playerId"`
	ProviderID                     string          `json:"providerId"`
	ReferenceExternalTransactionID string          `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        string          `json:"roundId"`
	WalletID                       string          `json:"walletId"`
}

// CanonicalJSON is the byte sequence the hash covers.
//
// The normalisations happen before this point, in the constructors that build the payload: the
// amount has exactly two places, the currency and the kind are upper case, the ids are the
// canonical lower case UUID form, and an absent reference is omitted rather than empty.
func (p OperationPayload) CanonicalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// HTML escaping would turn "<" and "&" into < and &: harmless, but a second
	// spelling of the same content is exactly what a canonical form must not have.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(p); err != nil {
		return nil, fmt.Errorf("encode the payload: %w", err)
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// Hash is the SHA-256 of the canonical JSON.
func (p OperationPayload) Hash() ([]byte, error) {
	canonical, err := p.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}
