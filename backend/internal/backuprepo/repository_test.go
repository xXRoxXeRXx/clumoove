package backuprepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestNormalizeRelativePath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		valid bool
	}{
		{name: "nested path", input: "documents/../photos/image.jpg", want: "photos/image.jpg", valid: true},
		{name: "empty", input: ""},
		{name: "absolute", input: "/etc/passwd"},
		{name: "parent", input: "../secret"},
		{name: "windows separator", input: `photos\image.jpg`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeRelativePath(test.input)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("NormalizeRelativePath(%q) = %q, %v; want %q, nil", test.input, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("NormalizeRelativePath(%q) unexpectedly succeeded as %q", test.input, got)
			}
		})
	}
}

func TestSplitBlocks(t *testing.T) {
	data := bytes.Repeat([]byte("a"), BlockSize+17)
	var blocks []Entry
	fileHash, err := SplitBlocks(context.Background(), bytes.NewReader(data), func(entry Entry) error {
		blocks = append(blocks, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("SplitBlocks returned error: %v", err)
	}
	if len(blocks) != 2 || len(blocks[0].Data) != BlockSize || len(blocks[1].Data) != 17 {
		t.Fatalf("SplitBlocks returned unexpected block boundaries")
	}
	if want := sha256.Sum256(data); fileHash != want {
		t.Fatalf("file hash = %x, want %x", fileHash, want)
	}
}

func TestSplitBlocksEmptyFile(t *testing.T) {
	called := false
	got, err := SplitBlocks(context.Background(), bytes.NewReader(nil), func(Entry) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("SplitBlocks returned error: %v", err)
	}
	if called {
		t.Fatal("SplitBlocks called callback for an empty file")
	}
	if want := sha256.Sum256(nil); got != want {
		t.Fatalf("empty file hash = %x, want %x", got, want)
	}
}

func TestValidatePack(t *testing.T) {
	first := []byte("first block")
	second := []byte("second block")
	entries := []Entry{{Hash: sha256.Sum256(first), Data: first}, {Hash: sha256.Sum256(second), Data: second}}
	var encoded bytes.Buffer
	pack, err := EncodePack(&encoded, entries)
	if err != nil {
		t.Fatalf("EncodePack returned error: %v", err)
	}

	var got []Entry
	var offsets []int64
	validated, err := ValidatePack(bytes.NewReader(encoded.Bytes()), pack.ID, func(offset int64, entry Entry) error {
		offsets = append(offsets, offset)
		got = append(got, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("ValidatePack returned error: %v", err)
	}
	if validated != pack {
		t.Fatalf("ValidatePack = %#v, want %#v", validated, pack)
	}
	if len(got) != len(entries) || !bytes.Equal(got[1].Data, second) {
		t.Fatal("ValidatePack did not return the encoded entries")
	}
	if want := []int64{packPrefixSize + sha256.Size + 4, packPrefixSize + sha256.Size + 4 + int64(len(first)) + sha256.Size + 4}; !slices.Equal(offsets, want) {
		t.Fatalf("ValidatePack payload offsets = %v, want %v", offsets, want)
	}
}

func TestValidatePackRejectsCorruption(t *testing.T) {
	payload := []byte("a block")
	var encoded bytes.Buffer
	pack, err := EncodePack(&encoded, []Entry{{Hash: sha256.Sum256(payload), Data: payload}})
	if err != nil {
		t.Fatalf("EncodePack returned error: %v", err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated", data: encoded.Bytes()[:encoded.Len()-1]},
		{name: "trailing", data: append(append([]byte(nil), encoded.Bytes()...), 1)},
		{name: "payload mutation", data: mutate(encoded.Bytes(), packPrefixSize+sha256.Size+4)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidatePack(bytes.NewReader(test.data), pack.ID, nil); err == nil {
				t.Fatal("ValidatePack accepted corrupt data")
			}
		})
	}
}

func TestValidatePackRejectsUnexpectedPackID(t *testing.T) {
	payload := []byte("a block")
	var encoded bytes.Buffer
	if _, err := EncodePack(&encoded, []Entry{{Hash: sha256.Sum256(payload), Data: payload}}); err != nil {
		t.Fatalf("EncodePack returned error: %v", err)
	}
	if _, err := ValidatePack(bytes.NewReader(encoded.Bytes()), [sha256.Size]byte{}, nil); err == nil {
		t.Fatal("ValidatePack accepted an unexpected pack ID")
	}
}

func TestEncodePackRejectsInvalidBlock(t *testing.T) {
	over := make([]byte, BlockSize+1)
	if _, err := EncodePack(io.Discard, []Entry{{Hash: sha256.Sum256(over), Data: over}}); err == nil {
		t.Fatal("EncodePack accepted a block larger than 4 MiB")
	}
	if _, err := EncodePack(io.Discard, []Entry{{Data: []byte("data")}}); err == nil {
		t.Fatal("EncodePack accepted a mismatched block hash")
	}
}

func TestSplitBlocksStopsOnCallbackError(t *testing.T) {
	want := errors.New("stop")
	_, err := SplitBlocks(context.Background(), bytes.NewReader([]byte("data")), func(Entry) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("SplitBlocks error = %v, want callback error", err)
	}
}

func mutate(data []byte, index int) []byte {
	mutated := append([]byte(nil), data...)
	mutated[index] ^= 0xff
	return mutated
}
