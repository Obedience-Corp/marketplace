package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type releaseTransport map[string][]byte

func (transport releaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	raw, found := transport[req.URL.String()]
	status := http.StatusOK
	if !found {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}, nil
}

func archiveFixture(t *testing.T, version, target, commit string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	binary := "camp-app"
	if strings.HasPrefix(target, "windows-") {
		binary += ".exe"
	}
	identity, _ := json.Marshal(map[string]string{"version": version, "target": target, "commit": commit})
	for _, file := range []struct{ name, contents string }{{binary, "fixture binary"}, {"README.md", "fixture readme"}, {"LICENSE", "fixture terms"}, {"release.json", string(identity)}} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0o755, Size: int64(len(file.contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, file.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func publicReleaseFixture(t *testing.T) (releaseTransport, publicRelease) {
	t.Helper()
	transport := make(releaseTransport)
	release := publicRelease{Tag: "camp-app-v0.1.0", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var checksums strings.Builder
	for _, target := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "windows-amd64", "linux-arm64"} {
		name := "camp-app-0.1.0-" + target + ".tar.gz"
		url := campAppReleaseRoot + release.Tag + "/" + name
		data := archiveFixture(t, "0.1.0", target, strings.Repeat("a", 40))
		transport[url] = data
		hash := sha256.Sum256(data)
		fmt.Fprintf(&checksums, "%x  %s\n", hash, name)
		release.Assets = append(release.Assets, releaseAsset{Name: name, URL: url})
	}
	checksumURL := campAppReleaseRoot + release.Tag + "/checksums.txt"
	transport[checksumURL] = []byte(checksums.String())
	release.Assets = append(release.Assets, releaseAsset{Name: "checksums.txt", URL: checksumURL})
	return transport, release
}

func setReleaseResponse(t *testing.T, transport releaseTransport, release publicRelease) {
	t.Helper()
	raw, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	transport["https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/camp-app-v0.1.0"] = raw
}

func catalogFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir() // These filesystem tests run only in the documented container lane.
	writeLayout(t, root, map[string]string{
		"obey-marketplace.json": `{"schema_version":"1","id":"official","namespace":"obedience-corp","name":"Official","packages":[{"id":"obedience-corp/camp-buzz","custom":"preserve me"}]}`,
		"index.json":            `{"source":"official-obey","updatedAt":"2026-01-01T00:00:00Z","packages":[{"id":"obedience-corp/camp-buzz","channels":["stable"]}]}`,
	})
	return root
}

func TestPrepareCampAppPreservesCatalogAndUsesActualVerifiedAssets(t *testing.T) {
	root := catalogFixture(t)
	transport, release := publicReleaseFixture(t)
	setReleaseResponse(t, transport, release)
	client := &http.Client{Transport: transport}
	if err := prepareCampApp(context.Background(), root, "0.1.0", client); err != nil {
		t.Fatal(err)
	}
	catalog, err := readJSONDocument(filepath.Join(root, "obey-marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	packages := catalog["packages"].([]any)
	if len(packages) != 2 || packages[0].(map[string]any)["custom"] != "preserve me" {
		t.Fatalf("unrelated catalog changed: %v", packages)
	}
	added := packages[1].(map[string]any)
	if _, exists := added["release_source"]; exists || added["manifest_path"] != "packages/obedience-corp/camp-app/obey-package.json" {
		t.Fatalf("entry requires a newer catalog schema: %v", added)
	}
	manifestPath := filepath.Join(root, "packages/obedience-corp/camp-app/obey-package.json")
	manifest, err := readJSONDocument(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	releases := manifest["releases"].([]any)
	artifacts := releases[0].(map[string]any)["artifacts"].([]any)
	if len(artifacts) != 5 {
		t.Fatalf("actual optional arm64 artifact lost: %v", artifacts)
	}
	for _, value := range artifacts {
		artifact := value.(map[string]any)
		digest := sha256.Sum256(transport[artifact["url"].(string)])
		if artifact["sha256"] != hex.EncodeToString(digest[:]) {
			t.Fatalf("hash was not derived from downloaded bytes: %v", artifact)
		}
	}
	paths := []string{manifestPath, filepath.Join(root, "obey-marketplace.json"), filepath.Join(root, "index.json")}
	snapshots := make(map[string][]byte)
	for _, path := range paths {
		snapshots[path], _ = os.ReadFile(path)
	}
	before, _ := os.ReadFile(manifestPath)
	if err := prepareCampApp(context.Background(), root, "0.1.0", client); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(manifestPath)
	if !bytes.Equal(before, after) {
		t.Fatal("identical released bytes should be idempotent")
	}
	for _, path := range paths {
		current, _ := os.ReadFile(path)
		if !bytes.Equal(snapshots[path], current) {
			t.Fatalf("idempotent preparation changed %s", path)
		}
	}
	// Optional isolated-harness artifact for exercising released Installer schemas.
	if output := os.Getenv("METADATA_TEST_EXPORT"); output != "" {
		for _, path := range paths {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(output, relative)
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A previous disk error may have written the manifest but not the index.
	writeLayout(t, root, map[string]string{"index.json": `{"source":"official-obey","updatedAt":"2026-01-01T00:00:00Z","packages":[{"id":"obedience-corp/camp-buzz","channels":["stable"]}]}`})
	if err := prepareCampApp(context.Background(), root, "0.1.0", client); err != nil {
		t.Fatal(err)
	}
	repaired, err := readJSONDocument(filepath.Join(root, "index.json"))
	if err != nil || len(repaired["packages"].([]any)) != 2 {
		t.Fatalf("partial preparation did not recover: %v %v", repaired, err)
	}
}

func TestPrepareCampAppRejectsUnpublishedOrInvalidArtifactsWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(releaseTransport, *publicRelease)
	}{
		{"missing public release", func(transport releaseTransport, _ *publicRelease) {
			delete(transport, "https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/camp-app-v0.1.0")
		}},
		{"draft", func(_ releaseTransport, release *publicRelease) { release.Draft = true }},
		{"prerelease", func(_ releaseTransport, release *publicRelease) { release.Prerelease = true }},
		{"wrong tag", func(_ releaseTransport, release *publicRelease) { release.Tag = "v0.1.0" }},
		{"missing target", func(_ releaseTransport, release *publicRelease) { release.Assets = release.Assets[1:] }},
		{"wrong asset host", func(_ releaseTransport, release *publicRelease) {
			release.Assets[0].URL = "https://untrusted.invalid/archive"
		}},
		{"checksum mismatch", func(transport releaseTransport, release *publicRelease) {
			transport[release.Assets[0].URL] = []byte("corrupt")
		}},
		{"duplicate checksums", func(transport releaseTransport, release *publicRelease) {
			url := campAppReleaseRoot + release.Tag + "/checksums.txt"
			transport[url] = append(transport[url], transport[url]...)
		}},
		{"wrong archive identity", func(transport releaseTransport, release *publicRelease) {
			asset := release.Assets[0]
			data := archiveFixture(t, "0.0.1", "darwin-arm64", strings.Repeat("a", 40))
			old := sha256.Sum256(transport[asset.URL])
			updated := sha256.Sum256(data)
			transport[asset.URL] = data
			url := campAppReleaseRoot + release.Tag + "/checksums.txt"
			transport[url] = bytes.ReplaceAll(transport[url], []byte(fmt.Sprintf("%x", old)), []byte(fmt.Sprintf("%x", updated)))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := catalogFixture(t)
			transport, release := publicReleaseFixture(t)
			setReleaseResponse(t, transport, release)
			test.mutate(transport, &release)
			if test.name != "missing public release" {
				setReleaseResponse(t, transport, release)
			}
			path := filepath.Join(root, "obey-marketplace.json")
			before, _ := os.ReadFile(path)
			if err := prepareCampApp(context.Background(), root, "0.1.0", &http.Client{Transport: transport}); err == nil {
				t.Fatal("invalid release accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed validation mutated the catalog")
			}
			if _, err := os.Stat(filepath.Join(root, "packages")); !os.IsNotExist(err) {
				t.Fatal("failed validation wrote a package manifest")
			}
		})
	}
}

func TestPrepareCampAppCancellationDoesNotWrite(t *testing.T) {
	root := catalogFixture(t)
	transport, release := publicReleaseFixture(t)
	setReleaseResponse(t, transport, release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareCampApp(ctx, root, "0.1.0", &http.Client{Transport: transport}); err == nil {
		t.Fatal("canceled release accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "packages")); !os.IsNotExist(err) {
		t.Fatal("cancellation wrote metadata")
	}
}

func TestPrepareCampAppRejectsMutatedPublishedVersion(t *testing.T) {
	root := catalogFixture(t)
	transport, release := publicReleaseFixture(t)
	setReleaseResponse(t, transport, release)
	client := &http.Client{Transport: transport}
	if err := prepareCampApp(context.Background(), root, "0.1.0", client); err != nil {
		t.Fatal(err)
	}
	paths := []string{"obey-marketplace.json", "index.json", "packages/obedience-corp/camp-app/obey-package.json"}
	snapshots := make(map[string][]byte)
	for _, path := range paths {
		snapshots[path], _ = os.ReadFile(filepath.Join(root, path))
	}
	var checksums strings.Builder
	for _, target := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "windows-amd64", "linux-arm64"} {
		name := "camp-app-0.1.0-" + target + ".tar.gz"
		data := archiveFixture(t, "0.1.0", target, strings.Repeat("b", 40))
		transport[campAppReleaseRoot+release.Tag+"/"+name] = data
		digest := sha256.Sum256(data)
		fmt.Fprintf(&checksums, "%x  %s\n", digest, name)
	}
	transport[campAppReleaseRoot+release.Tag+"/checksums.txt"] = []byte(checksums.String())
	err := prepareCampApp(context.Background(), root, "0.1.0", client)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("mutated published bytes accepted: %v", err)
	}
	for _, path := range paths {
		after, _ := os.ReadFile(filepath.Join(root, path))
		if !bytes.Equal(snapshots[path], after) {
			t.Fatalf("immutable version mutation changed %s", path)
		}
	}
}
