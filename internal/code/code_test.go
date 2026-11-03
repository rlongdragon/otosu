package code

import (
	"errors"
	"strings"
	"testing"

	"github.com/rlongdragon/otosu/internal/wire"
)

func TestNewIsValidAndVaries(t *testing.T) {
	seen := make(map[string]bool, 512)
	for i := 0; i < 512; i++ {
		c, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if !Valid(c) {
			t.Fatalf("New produced invalid code %q", c)
		}
		seen[c] = true
	}
	// 40 bits of entropy: 512 draws should essentially never collide.
	if len(seen) != 512 {
		t.Errorf("got %d distinct codes out of 512", len(seen))
	}
}

func TestNewUsesFullAlphabet(t *testing.T) {
	used := make(map[rune]bool, len(Alphabet))
	for i := 0; i < 4000; i++ {
		c, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		for _, r := range c {
			used[r] = true
		}
	}
	// A biased mapping would leave part of the alphabet unreachable.
	if len(used) != len(Alphabet) {
		t.Errorf("used %d of %d alphabet characters", len(used), len(Alphabet))
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"b4k7qw2m", "B4K7QW2M"},
		{"B4K7-QW2M", "B4K7QW2M"},
		{"  B4K7QW2M  ", "B4K7QW2M"},
		{"IL0O", "1100"}, // I and L fold to 1, O folds to 0
		{"b4k7 qw2m", "B4K7QW2M"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"B4K7QW2M", true},
		{"00000000", true},
		{"B4K7QW2", false},   // too short
		{"B4K7QW2MX", false}, // too long
		{"B4K7QW2I", false},  // I is not in the alphabet
		{"b4k7qw2m", false},  // Valid expects normalized input
		{"B4K7QW2!", false},
	} {
		if got := Valid(tc.in); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRefRoundTrip(t *testing.T) {
	key, err := wire.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ref := Ref{Code: c, Key: key}
	got, err := ParseRef(ref.String())
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", ref.String(), err)
	}
	if got.Code != ref.Code {
		t.Errorf("Code = %q, want %q", got.Code, ref.Code)
	}
	if string(got.Key) != string(ref.Key) {
		t.Error("key did not survive the round trip")
	}
}

func TestParseRefAcceptsURL(t *testing.T) {
	key, _ := wire.NewKey()
	ref := Ref{Code: "B4K7QW2M", Key: key}

	got, err := ParseRef("https://otosu.example.com/d/" + ref.String())
	if err != nil {
		t.Fatalf("ParseRef on full URL: %v", err)
	}
	if got.Code != ref.Code || string(got.Key) != string(ref.Key) {
		t.Error("URL form did not parse to the same reference")
	}
}

func TestParseRefNormalizesCode(t *testing.T) {
	key, _ := wire.NewKey()
	ref := Ref{Code: "B4K7QW2M", Key: key}

	got, err := ParseRef(strings.ToLower(ref.String()[:8]) + ref.String()[8:])
	if err != nil {
		t.Fatalf("ParseRef with lowercase code: %v", err)
	}
	if got.Code != "B4K7QW2M" {
		t.Errorf("Code = %q, want B4K7QW2M", got.Code)
	}
}

func TestParseRefErrors(t *testing.T) {
	key, _ := wire.NewKey()
	valid := Ref{Code: "B4K7QW2M", Key: key}.String()

	t.Run("no key", func(t *testing.T) {
		if _, err := ParseRef("B4K7QW2M"); !errors.Is(err, ErrNoKey) {
			t.Fatalf("err = %v, want ErrNoKey", err)
		}
	})
	t.Run("bad code", func(t *testing.T) {
		if _, err := ParseRef("SHORT" + valid[8:]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	})
	t.Run("short key", func(t *testing.T) {
		if _, err := ParseRef("B4K7QW2M#aGVsbG8"); err == nil {
			t.Fatal("accepted a key that is not 32 bytes")
		}
	})
	t.Run("bad key encoding", func(t *testing.T) {
		if _, err := ParseRef("B4K7QW2M#not!valid!base64"); err == nil {
			t.Fatal("accepted a malformed key")
		}
	})
}

func TestFindRefs(t *testing.T) {
	a := Ref{Code: "B4K7QW2M", Key: make([]byte, wire.KeySize)}.String()
	b := Ref{Code: "DH48TNX7", Key: []byte(strings.Repeat("k", wire.KeySize))}.String()

	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"none", "what is wrong here?", nil},
		{"alone", a, []string{"B4K7QW2M"}},
		{"inside a sentence", "看這張圖 " + a + "，哪裡錯了", []string{"B4K7QW2M"}},
		{"two, in order", b + " and " + a, []string{"DH48TNX7", "B4K7QW2M"}},
		{"duplicate collected once", a + " " + a, []string{"B4K7QW2M"}},
		{"full URL", "https://otosu.example.com/" + a, []string{"B4K7QW2M"}},
		{"glued to a longer token", "x" + a, nil},
		{"key too short", "B4K7QW2M#aGVsbG8", nil},
		{"key too long", a + "A", nil},
		{"invalid code", "B4K7QW2U#" + strings.Repeat("A", 43), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range FindRefs(tc.text) {
				got = append(got, r.Code)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("FindRefs(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
