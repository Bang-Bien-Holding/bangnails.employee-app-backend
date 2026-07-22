package tokenx

import (
	"encoding/hex"
	"testing"
)

func TestGenerate(t *testing.T) {
	token, err := Generate(32)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	decoded, err := hex.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not valid hex: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("decoded token length = %d, want 32", len(decoded))
	}

	other, err := Generate(32)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token == other {
		t.Error("expected two generated tokens to differ")
	}
}

func TestHash(t *testing.T) {
	// Known SHA-256 digest of "sometoken", cross-checked independently —
	// pins Hash to the actual SHA-256 hex encoding, not just "looks like a
	// hash".
	const want = "9c928547a5dce2fcc2788de34fe163942f64e50fea570d33774822d5eacbf1ec"
	if got := Hash("sometoken"); got != want {
		t.Errorf("Hash(%q) = %q, want %q", "sometoken", got, want)
	}

	if Hash("othertoken") == Hash("sometoken") {
		t.Error("expected different inputs to hash differently")
	}
}
