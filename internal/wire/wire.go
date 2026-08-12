// Package wire defines the end-to-end encrypted envelope exchanged between the
// browser and the otosu client.
//
// The server only ever sees the sealed blob. It never receives the key, so it
// cannot recover the file contents or even the file name.
//
// Sealed blob layout:
//
//	[12]byte  nonce
//	[...]byte AES-256-GCM(plaintext), tag included
//
// Plaintext layout:
//
//	[2]byte   big-endian header length
//	[n]byte   header, JSON encoded
//	[...]byte file contents
package wire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// KeySize is the AES-256 key length in bytes.
	KeySize = 32
	// NonceSize is the GCM nonce length in bytes.
	NonceSize = 12
	// MaxHeaderSize bounds the JSON header so a malformed blob cannot make us
	// allocate wildly before authentication has been checked.
	MaxHeaderSize = 8 << 10
)

// Header carries the metadata needed to reconstruct the file on the client.
// It is inside the sealed plaintext, so the file name never reaches the server.
type Header struct {
	Name string `json:"n"`
	Mime string `json:"m,omitempty"`
	Size int64  `json:"s"`
}

var (
	// ErrKeySize is returned when a key is not exactly KeySize bytes.
	ErrKeySize = errors.New("wire: key must be 32 bytes")
	// ErrMalformed is returned for a blob that cannot be parsed at all.
	ErrMalformed = errors.New("wire: malformed blob")
	// ErrAuth is returned when GCM authentication fails, meaning a wrong key or
	// tampered data.
	ErrAuth = errors.New("wire: authentication failed (wrong key or corrupted data)")
)

// NewKey returns a fresh random AES-256 key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("wire: generate key: %w", err)
	}
	return k, nil
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wire: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("wire: new gcm: %w", err)
	}
	return gcm, nil
}

// Seal encrypts hdr and data into a single blob. It mirrors the browser's
// WebCrypto implementation; both sides must stay byte compatible.
func Seal(key []byte, hdr Header, data []byte) ([]byte, error) {
	gcm, err := aead(key)
	if err != nil {
		return nil, err
	}

	hdr.Size = int64(len(data))
	meta, err := json.Marshal(hdr)
	if err != nil {
		return nil, fmt.Errorf("wire: marshal header: %w", err)
	}
	if len(meta) > MaxHeaderSize {
		return nil, fmt.Errorf("wire: header too large (%d bytes)", len(meta))
	}

	plain := make([]byte, 0, 2+len(meta)+len(data))
	plain = binary.BigEndian.AppendUint16(plain, uint16(len(meta)))
	plain = append(plain, meta...)
	plain = append(plain, data...)

	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("wire: generate nonce: %w", err)
	}

	// Prefix the nonce so Open can recover it, and let GCM append the ciphertext
	// to the same buffer.
	out := make([]byte, NonceSize, NonceSize+len(plain)+gcm.Overhead())
	copy(out, nonce)
	return gcm.Seal(out, nonce, plain, nil), nil
}

// Open authenticates and decrypts a blob produced by Seal or by the browser.
func Open(key []byte, blob []byte) (Header, []byte, error) {
	gcm, err := aead(key)
	if err != nil {
		return Header{}, nil, err
	}
	if len(blob) < NonceSize+gcm.Overhead() {
		return Header{}, nil, ErrMalformed
	}

	nonce, ct := blob[:NonceSize], blob[NonceSize:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return Header{}, nil, ErrAuth
	}
	if len(plain) < 2 {
		return Header{}, nil, ErrMalformed
	}

	n := int(binary.BigEndian.Uint16(plain[:2]))
	if n > MaxHeaderSize || 2+n > len(plain) {
		return Header{}, nil, ErrMalformed
	}

	var hdr Header
	if err := json.Unmarshal(plain[2:2+n], &hdr); err != nil {
		return Header{}, nil, fmt.Errorf("%w: header: %v", ErrMalformed, err)
	}
	return hdr, plain[2+n:], nil
}
