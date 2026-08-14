package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestSignAndVerifyAll(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "packages")
	pkgDir := filepath.Join(root, "acme", "demo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(pkgDir, "obey-package.json")
	if err := os.WriteFile(manifest, []byte("{\n  \"z\": 2, \"a\": 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	private := base64.StdEncoding.EncodeToString(priv)
	public := base64.StdEncoding.EncodeToString(pub)
	if err := signAll(root, "test-key", private); err != nil {
		t.Fatalf("signAll: %v", err)
	}
	if err := verifyAll(root, "test-key", public); err != nil {
		t.Fatalf("verifyAll: %v", err)
	}
	if err := os.WriteFile(manifest, []byte(`{"a":1,"z":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyAll(root, "test-key", public); err == nil {
		t.Fatal("tampered manifest unexpectedly verified")
	}
}

func TestCanonicalizeRejectsTrailingDocument(t *testing.T) {
	if _, err := canonicalize([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Fatal("trailing JSON document accepted")
	}
}
