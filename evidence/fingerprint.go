package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

var digestPattern = regexp.MustCompile(`\A[0-9a-f]{64}\z`)

// ValidDigest reports whether s is a lowercase hexadecimal SHA-256 digest.
func ValidDigest(s string) bool { return digestPattern.MatchString(s) }

// Digest returns the lowercase hexadecimal SHA-256 of body.
func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Canonical returns the canonical JSON encoding of v: object keys sorted,
// numbers preserved exactly as encoded, no HTML escaping and no insignificant
// whitespace. A struct and a map holding the same JSON value encode equally.
func Canonical(v any) ([]byte, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: fingerprint value: %w", ErrInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, fmt.Errorf("%w: fingerprint value: %w", ErrInvalid, err)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(generic); err != nil {
		return nil, fmt.Errorf("%w: fingerprint value: %w", ErrInvalid, err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// Fingerprint returns the SHA-256 of the canonical JSON encoding of v.
func Fingerprint(v any) (string, error) {
	body, err := Canonical(v)
	if err != nil {
		return "", err
	}
	return Digest(body), nil
}
