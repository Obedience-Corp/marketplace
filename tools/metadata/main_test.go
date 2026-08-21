package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeLayout(t *testing.T, root string, layout map[string]string) {
	t.Helper()
	for rel, contents := range layout {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTargets(t *testing.T) {
	tests := []struct {
		name    string
		layout  map[string]string // relative path -> contents
		want    []string          // relative paths, in order
		wantErr string
	}{
		{
			name:    "empty tree is an error",
			layout:  map[string]string{},
			wantErr: "no metadata documents found",
		},
		{
			name:   "packages only",
			layout: map[string]string{"packages/ns/p/obey-package.json": "{}"},
			want:   []string{"packages/ns/p/obey-package.json"},
		},
		{
			name: "root documents come first",
			layout: map[string]string{
				"index.json":                      "{}",
				"obey-marketplace.json":           "{}",
				"packages/ns/p/obey-package.json": "{}",
			},
			want: []string{
				"obey-marketplace.json",
				"index.json",
				"packages/ns/p/obey-package.json",
			},
		},
		{
			name: "keys and tools are never targets",
			layout: map[string]string{
				"obey-marketplace.json":        "{}",
				"keys/somekey.pub":             "x",
				"tools/metadata/testdata.json": "{}",
			},
			want: []string{"obey-marketplace.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeLayout(t, root, tt.layout)

			got, err := targets(root)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("targets() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("targets(): %v", err)
			}
			rel := make([]string, len(got))
			for i, p := range got {
				r, err := filepath.Rel(root, p)
				if err != nil {
					t.Fatal(err)
				}
				rel[i] = filepath.ToSlash(r)
			}
			if len(rel) != len(tt.want) {
				t.Fatalf("targets() = %v, want %v", rel, tt.want)
			}
			for i := range rel {
				if rel[i] != tt.want[i] {
					t.Fatalf("targets() = %v, want %v", rel, tt.want)
				}
			}
		})
	}
}

func TestVerifyAll_MissingSignature(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, map[string]string{
		"obey-marketplace.json": "{\n  \"z\": 2, \"a\": 1\n}\n",
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private := base64.StdEncoding.EncodeToString(priv)
	public := base64.StdEncoding.EncodeToString(pub)
	if err := signAll(root, "test-key", private); err != nil {
		t.Fatalf("signAll: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "obey-marketplace.json.sig")); err != nil {
		t.Fatal(err)
	}
	err = verifyAll(root, "test-key", public)
	if err == nil || !strings.Contains(err.Error(), "obey-marketplace.json.sig") {
		t.Fatalf("verifyAll() error = %v, want it to mention obey-marketplace.json.sig", err)
	}
}

func TestVerifyAll_NonCanonical(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, map[string]string{
		"index.json": "{\n  \"a\": 1\n}\n",
	})
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.StdEncoding.EncodeToString(pub)
	err = verifyAll(root, "test-key", public)
	if err == nil || !strings.HasSuffix(err.Error(), "index.json is not canonical JSON") {
		t.Fatalf("verifyAll() error = %v, want it to end with %q", err, "index.json is not canonical JSON")
	}
}

func TestVerifyAll_TamperedAfterSigning(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, map[string]string{
		"obey-marketplace.json": "{\n  \"z\": 2, \"a\": 1\n}\n",
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private := base64.StdEncoding.EncodeToString(priv)
	public := base64.StdEncoding.EncodeToString(pub)
	if err := signAll(root, "test-key", private); err != nil {
		t.Fatalf("signAll: %v", err)
	}
	manifest := filepath.Join(root, "obey-marketplace.json")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, append(raw, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	// Appending a byte to an already-canonical file breaks the "is this
	// canonical JSON" check before signature verification ever runs,
	// because canonicalize() re-encodes and the trailing byte does not
	// round-trip. That is the error this asserts.
	err = verifyAll(root, "test-key", public)
	if err == nil || !strings.Contains(err.Error(), "not canonical JSON") {
		t.Fatalf("verifyAll() error = %v, want it to mention canonical JSON", err)
	}
}

func TestVerifyAll_WrongKeyID(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, map[string]string{
		"obey-marketplace.json": "{\n  \"z\": 2, \"a\": 1\n}\n",
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private := base64.StdEncoding.EncodeToString(priv)
	public := base64.StdEncoding.EncodeToString(pub)
	if err := signAll(root, "test-key", private); err != nil {
		t.Fatalf("signAll: %v", err)
	}
	sigPath := filepath.Join(root, "obey-marketplace.json.sig")
	sigRaw, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	var sig detachedSignature
	if err := json.Unmarshal(sigRaw, &sig); err != nil {
		t.Fatal(err)
	}
	sig.KeyID = "wrong-key"
	rewritten, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	err = verifyAll(root, "test-key", public)
	if err == nil || !strings.Contains(err.Error(), "uses unexpected key or algorithm") {
		t.Fatalf("verifyAll() error = %v, want it to mention unexpected key or algorithm", err)
	}
}

func TestSignAndVerifyAll(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, map[string]string{
		"obey-marketplace.json":                "{\n  \"y\": 2, \"x\": 1\n}\n",
		"index.json":                           "{\n  \"b\": 2, \"a\": 1\n}\n",
		"packages/acme/demo/obey-package.json": "{\n  \"z\": 2, \"a\": 1\n}\n",
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
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

	want, err := targets(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(want)
	for _, p := range want {
		if _, err := os.Stat(p + ".sig"); err != nil {
			t.Fatalf("expected signature for %s: %v", p, err)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		canonicalRaw, err := canonicalize(raw)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != string(canonicalRaw) {
			t.Fatalf("%s is not byte-identical to its canonical form", p)
		}
	}

	manifest := filepath.Join(root, "packages", "acme", "demo", "obey-package.json")
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
