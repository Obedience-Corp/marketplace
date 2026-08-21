package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	canonical "github.com/gibson042/canonicaljson-go"
)

const (
	officialKeyID     = "obedience-marketplace-2026-01"
	officialAlgorithm = "ed25519"
	officialPublicKey = "B6DUhrEgXcXGIWThyI1oe5k/iWg8h2pMLuXFx8QtOQw="
	privateKeyEnv     = "FESTIVAL_METADATA_SIGNING_KEY"
)

type detachedSignature struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "sign" && os.Args[1] != "verify") {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/metadata <sign|verify>")
		os.Exit(2)
	}
	var err error
	if os.Args[1] == "sign" {
		err = signAll(".", officialKeyID, os.Getenv(privateKeyEnv))
	} else {
		err = verifyOfficial(".")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func signAll(root, keyID, encodedPrivate string) error {
	priv, err := decodePrivateKey(encodedPrivate)
	if err != nil {
		return err
	}
	return forEachTarget(root, func(path string) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		canonicalRaw, err := canonicalize(raw)
		if err != nil {
			return fmt.Errorf("canonicalize %s: %w", path, err)
		}
		sigRaw, err := json.Marshal(detachedSignature{
			KeyID:     keyID,
			Algorithm: officialAlgorithm,
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, canonicalRaw)),
		})
		if err != nil {
			return fmt.Errorf("encode %s.sig: %w", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if err := writeAtomic(path, canonicalRaw, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := writeAtomic(path+".sig", append(sigRaw, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s.sig: %w", path, err)
		}
		fmt.Printf("signed %s with %s\n", path, keyID)
		return nil
	})
}

func verifyOfficial(root string) error {
	keyFile, err := os.ReadFile(filepath.Join("keys", officialKeyID+".pub"))
	if err != nil {
		return fmt.Errorf("read public key file: %w", err)
	}
	if strings.TrimSpace(string(keyFile)) != officialPublicKey {
		return errors.New("public key file does not match verifier key")
	}
	return verifyAll(root, officialKeyID, officialPublicKey)
}

func verifyAll(root, keyID, encodedPublic string) error {
	pubRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedPublic))
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return errors.New("public key is invalid")
	}
	return forEachTarget(root, func(path string) error {
		manifest, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		canonicalRaw, err := canonicalize(manifest)
		if err != nil {
			return fmt.Errorf("canonicalize %s: %w", path, err)
		}
		if !bytes.Equal(manifest, canonicalRaw) {
			return fmt.Errorf("%s is not canonical JSON", path)
		}
		sigRaw, err := os.ReadFile(path + ".sig")
		if err != nil {
			return fmt.Errorf("read %s.sig: %w", path, err)
		}
		var sig detachedSignature
		if err := json.Unmarshal(sigRaw, &sig); err != nil {
			return fmt.Errorf("decode %s.sig: %w", path, err)
		}
		if sig.KeyID != keyID || sig.Algorithm != officialAlgorithm {
			return fmt.Errorf("%s.sig uses unexpected key or algorithm", path)
		}
		bytes, err := base64.StdEncoding.DecodeString(sig.Signature)
		if err != nil {
			return fmt.Errorf("decode %s.sig signature: %w", path, err)
		}
		if !ed25519.Verify(ed25519.PublicKey(pubRaw), manifest, bytes) {
			return fmt.Errorf("%s signature verification failed", path)
		}
		fmt.Printf("verified %s with %s\n", path, keyID)
		return nil
	})
}

// targets returns every metadata document that must carry a detached
// signature, in a stable order: the two root documents first, then every
// package manifest in path order. Only these three classes are signed; keys/
// and tools/ are never signing targets.
//
// The root files are optional but the set is not: a repository with neither
// root file and no packages is an error (the old "no targets found" guard),
// but a repository with packages and no index.json is fine, because
// index.json is not required by any schema the hub enforces.
func targets(root string) ([]string, error) {
	var out []string

	for _, name := range []string{"obey-marketplace.json", "index.json"} {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
	}

	pkgRoot := filepath.Join(root, "packages")
	err := filepath.WalkDir(pkgRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && path == pkgRoot {
				return fs.SkipAll
			}
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "obey-package.json" {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no metadata documents found")
	}
	return out, nil
}

func forEachTarget(root string, fn func(path string) error) error {
	paths, err := targets(root)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func canonicalize(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON document contains trailing data")
		}
		return nil, err
	}
	return canonical.Marshal(value)
}

func decodePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, errors.New("private signing key is not valid base64")
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, errors.New("private signing key has invalid length")
	}
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".marketplace-metadata-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
