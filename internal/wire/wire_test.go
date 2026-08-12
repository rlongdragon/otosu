package wire

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := mustKey(t)
	data := []byte("screenshot bytes \x00\x01\xff and more")
	hdr := Header{Name: "shot.png", Mime: "image/png"}

	blob, err := Seal(key, hdr, data)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(blob, data) {
		t.Fatal("plaintext leaked into the sealed blob")
	}
	if bytes.Contains(blob, []byte("shot.png")) {
		t.Fatal("file name leaked into the sealed blob")
	}

	gotHdr, gotData, err := Open(key, blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if gotHdr.Name != hdr.Name || gotHdr.Mime != hdr.Mime {
		t.Errorf("header = %+v, want name=%q mime=%q", gotHdr, hdr.Name, hdr.Mime)
	}
	if gotHdr.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", gotHdr.Size, len(data))
	}
	if !bytes.Equal(gotData, data) {
		t.Errorf("data = %q, want %q", gotData, data)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	key := mustKey(t)
	a, err := Seal(key, Header{Name: "x"}, []byte("same"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := Seal(key, Header{Name: "x"}, []byte("same"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same input produced identical blobs; nonce is not random")
	}
}

func TestOpenWrongKey(t *testing.T) {
	blob, err := Seal(mustKey(t), Header{Name: "x"}, []byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, _, err := Open(mustKey(t), blob); !errors.Is(err, ErrAuth) {
		t.Fatalf("Open with wrong key: err = %v, want ErrAuth", err)
	}
}

func TestOpenTampered(t *testing.T) {
	key := mustKey(t)
	blob, err := Seal(key, Header{Name: "x"}, []byte("secret payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for _, tc := range []struct {
		name string
		pos  int
	}{
		{"nonce", 0},
		{"ciphertext", NonceSize + 4},
		{"tag", len(blob) - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := bytes.Clone(blob)
			bad[tc.pos] ^= 0x01
			if _, _, err := Open(key, bad); !errors.Is(err, ErrAuth) {
				t.Fatalf("Open on tampered %s: err = %v, want ErrAuth", tc.name, err)
			}
		})
	}
}

func TestOpenMalformed(t *testing.T) {
	key := mustKey(t)
	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"empty", nil},
		{"shorter than nonce", make([]byte, NonceSize-1)},
		{"nonce only", make([]byte, NonceSize)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Open(key, tc.blob); err == nil {
				t.Fatal("Open succeeded on malformed blob")
			}
		})
	}
}

func TestBadKeySize(t *testing.T) {
	if _, err := Seal(make([]byte, 16), Header{}, nil); !errors.Is(err, ErrKeySize) {
		t.Fatalf("Seal with 16 byte key: err = %v, want ErrKeySize", err)
	}
	if _, _, err := Open(make([]byte, 16), make([]byte, 64)); !errors.Is(err, ErrKeySize) {
		t.Fatalf("Open with 16 byte key: err = %v, want ErrKeySize", err)
	}
}

func TestLargePayload(t *testing.T) {
	key := mustKey(t)
	data := make([]byte, 4<<20) // 4 MiB, a plausible 4K screenshot
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}

	blob, err := Seal(key, Header{Name: "4k.png", Mime: "image/png"}, data)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, got, err := Open(key, blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("4 MiB round trip corrupted the payload")
	}
}
