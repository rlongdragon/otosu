// Package code handles the short retrieval codes and the code#key references
// the browser hands to the user.
//
// A reference looks like:
//
//	B4K7QW2M#0hV1n...   <- 8 char code, then the base64url AES key
//
// Only the part before '#' is ever sent to the server. The key stays with the
// user, which is what keeps the drop end-to-end encrypted.
package code

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/rlongdragon/otosu/internal/wire"
)

// Alphabet is Crockford base32: the digits and letters minus I, L, O and U, so
// codes cannot be misread when they are printed or spoken.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Length is the number of characters in a code. Eight Crockford characters is
// 40 bits, which combined with a short TTL and request rate limiting puts brute
// force out of reach.
const Length = 8

var (
	// ErrInvalid is returned when a string is not a well formed code.
	ErrInvalid = errors.New("code: invalid code")
	// ErrNoKey is returned when a reference is missing its '#key' half.
	ErrNoKey = errors.New("code: reference has no key")
)

// keyEncoding is unpadded base64url so the key survives a URL fragment intact.
var keyEncoding = base64.RawURLEncoding

// New returns a fresh random code.
func New() (string, error) {
	buf := make([]byte, Length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("code: read random: %w", err)
	}
	out := make([]byte, Length)
	for i, b := range buf {
		// len(Alphabet) is 32 and divides 256 evenly, so the modulo is unbiased.
		out[i] = Alphabet[int(b)%len(Alphabet)]
	}
	return string(out), nil
}

// Normalize canonicalises user supplied input: it uppercases, drops the hyphens
// people insert for readability, and folds the characters Crockford treats as
// ambiguous.
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch r {
		case '-', ' ':
			// Cosmetic separators.
		case 'I', 'L':
			b.WriteRune('1')
		case 'O':
			b.WriteRune('0')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Valid reports whether s is a syntactically correct code. It says nothing
// about whether the drop still exists.
func Valid(s string) bool {
	if len(s) != Length {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(Alphabet, r) {
			return false
		}
	}
	return true
}

// Ref is a code paired with the key needed to decrypt the drop it names.
type Ref struct {
	Code string
	Key  []byte
}

// ParseRef reads a "CODE#KEY" reference. The code half is normalized, so input
// copied by hand still works.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	// Tolerate a full URL: everything before the last '/' is noise to us.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}

	rawCode, rawKey, ok := strings.Cut(s, "#")
	if !ok {
		return Ref{}, ErrNoKey
	}

	c := Normalize(rawCode)
	if !Valid(c) {
		return Ref{}, fmt.Errorf("%w: %q", ErrInvalid, rawCode)
	}

	key, err := keyEncoding.DecodeString(strings.TrimSpace(rawKey))
	if err != nil {
		return Ref{}, fmt.Errorf("code: decode key: %w", err)
	}
	if len(key) != wire.KeySize {
		return Ref{}, fmt.Errorf("code: key must be %d bytes, got %d", wire.KeySize, len(key))
	}
	return Ref{Code: c, Key: key}, nil
}

// String renders the reference in the form the browser copies to the clipboard.
func (r Ref) String() string {
	return r.Code + "#" + keyEncoding.EncodeToString(r.Key)
}
