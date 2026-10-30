package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishedVersionUsesProductNamespaceAndSemver(t *testing.T) {
	route := releaseRoutes["camp-app"]
	transport := releaseTransport{}
	first := make([]publicRelease, 100)
	for i := range first {
		first[i] = publicRelease{Tag: "app-test-ignore"}
	}
	first[0] = publicRelease{Tag: "camp-app-v0.9.0", PublishedAt: time.Now()}
	first[1] = publicRelease{Tag: "camp-app-v9.0.0", PublishedAt: time.Now(), Draft: true}
	first[2] = publicRelease{Tag: "camp-app-v8.0.0", PublishedAt: time.Now(), Prerelease: true}
	first[3] = publicRelease{Tag: "v99.0.0", PublishedAt: time.Now()}
	first[4] = publicRelease{Tag: "camp-app-v7.0.0"}
	transport["https://api.github.com/repos/Obedience-Corp/obey-releases/releases?per_page=100&page=1"], _ = json.Marshal(first)
	transport["https://api.github.com/repos/Obedience-Corp/obey-releases/releases?per_page=100&page=2"], _ = json.Marshal([]publicRelease{{Tag: "camp-app-v0.10.0", PublishedAt: time.Now()}})
	got, err := latestPublishedVersion(context.Background(), &http.Client{Transport: transport}, route)
	if err != nil || got != "0.10.0" {
		t.Fatalf("latest = %q, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := latestPublishedVersion(ctx, &http.Client{Transport: transport}, route); err == nil {
		t.Fatal("cancelled lookup succeeded")
	}
}

func TestReconcileDuplicateAndDelayedReleasePreserveCatalog(t *testing.T) {
	root := catalogFixture(t)
	transport, release := publicReleaseFixture(t)
	setReleaseResponse(t, transport, release)
	client := &http.Client{Transport: transport}
	if err := reconcileRelease(context.Background(), root, "camp-app", "0.1.0", client); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "packages/obedience-corp/camp-app/obey-package.json")
	before, _ := os.ReadFile(path)
	for _, version := range []string{"0.1.0", "0.0.9"} {
		// No network is necessary once the catalog supersedes the event.
		if err := reconcileRelease(context.Background(), root, "camp-app", version, &http.Client{Transport: releaseTransport{}}); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatal("duplicate/delayed event changed catalog")
		}
	}
	if err := reconcileRelease(context.Background(), root, "unknown", "0.1.0", client); err == nil {
		t.Fatal("unregistered producer accepted")
	}
}

func suiteReleaseFixture(t *testing.T) (releaseTransport, string) {
	t.Helper()
	version, tag := "1.2.0", "v1.2.0"
	base := "https://github.com/Obedience-Corp/festival/releases/download/" + tag + "/"
	transport := releaseTransport{}
	published := publicRelease{Tag: tag, PublishedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
	var checksums strings.Builder
	var artifacts []any
	for _, target := range []string{"macOS-all.tar.gz", "linux-arm64.tar.gz", "linux-x86_64.tar.gz", "windows-x86_64.zip"} {
		name := "festival-" + version + "-" + target
		payload := []byte("verified suite fixture " + target)
		digest := fmt.Sprintf("%x", sha256.Sum256(payload))
		transport[base+name] = payload
		fmt.Fprintf(&checksums, "%s  %s\n", digest, name)
		artifacts = append(artifacts, map[string]any{"filename": name, "url": base + name, "sha256": digest})
		published.Assets = append(published.Assets, releaseAsset{Name: name, URL: base + name})
	}
	doc := map[string]any{"id": "obedience-corp/festival", "class": "product", "releases": []any{map[string]any{
		"version": version, "channel": "stable", "published_at": published.PublishedAt.Format(time.RFC3339), "artifacts": artifacts,
	}}}
	transport[base+"obey-package.json"], _ = json.Marshal(doc)
	transport[base+"checksums.txt"] = []byte(checksums.String())
	for _, name := range []string{"checksums.txt", "obey-package.json"} {
		published.Assets = append(published.Assets, releaseAsset{Name: name, URL: base + name})
	}
	transport["https://api.github.com/repos/Obedience-Corp/festival/releases/tags/"+tag], _ = json.Marshal(published)
	return transport, base
}

func TestSuiteReconciliationPreservesHistoryAndRejectsIncompleteBytes(t *testing.T) {
	for _, failure := range []string{"missing", "checksum", "foreign-url", "success"} {
		t.Run(failure, func(t *testing.T) {
			root := catalogFixture(t)
			path := filepath.Join(root, "packages/obedience-corp/festival/obey-package.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			before := `{"id":"obedience-corp/festival","summary":"retain me","releases":[{"version":"1.1.0","channel":"stable"}]}`
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
				t.Fatal(err)
			}
			transport, base := suiteReleaseFixture(t)
			switch failure {
			case "missing":
				delete(transport, base+"festival-1.2.0-windows-x86_64.zip")
			case "checksum":
				transport[base+"festival-1.2.0-linux-arm64.tar.gz"] = []byte("changed")
			case "foreign-url":
				transport[base+"obey-package.json"] = []byte(strings.ReplaceAll(string(transport[base+"obey-package.json"]), base+"festival-1.2.0-linux-arm64.tar.gz", "https://example.invalid/foreign"))
			}
			err := reconcileRelease(context.Background(), root, "festival", "1.2.0", &http.Client{Transport: transport})
			after, _ := os.ReadFile(path)
			if failure != "success" {
				if err == nil || string(after) != before {
					t.Fatalf("failure %s: err=%v, changed=%v", failure, err, string(after) != before)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			doc, err := readJSONDocument(path)
			if err != nil || len(doc["releases"].([]any)) != 2 || doc["summary"] != "retain me" {
				t.Fatalf("history changed: %v, %v", doc, err)
			}
			// Preparation itself also enforces immutability for a repeated version.
			if err := prepareFestival(context.Background(), root, "1.2.0", &http.Client{Transport: transport}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
