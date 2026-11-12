package main

import (
	"bytes"
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

const festivalAppAPI = "https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/app-v0.1.8"

func festivalAppFixture(t *testing.T) (releaseTransport, publicRelease) {
	t.Helper()
	return festivalAppFixtureFor(t, "0.1.8")
}

func festivalAppFixtureFor(t *testing.T, v string) (releaseTransport, publicRelease) {
	t.Helper()
	transport := make(releaseTransport)
	release := publicRelease{Tag: "app-v" + v, PublishedAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	root := campAppReleaseRoot + release.Tag + "/"
	add := func(name string, data []byte) {
		transport[root+name] = data
		release.Assets = append(release.Assets, releaseAsset{Name: name, URL: root + name})
	}
	var sums strings.Builder
	feed := updaterFeed{Version: v, Platforms: map[string]struct {
		Signature string `json:"signature"`
		URL       string `json:"url"`
	}{}}
	for _, target := range festivalAppTargets {
		name := "Festival.App_" + v + target.Suffix
		data := []byte("fixture bytes for " + name)
		sig := "signature for " + name
		add(name, data)
		add(name+".sig", []byte(sig))
		digest := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%x  %s\n", digest, name)
		feed.Platforms[target.Platform] = struct {
			Signature string `json:"signature"`
			URL       string `json:"url"`
		}{sig, root + name}
	}
	add("Festival.App_"+v+"_aarch64.dmg", []byte("ignored dmg"))
	add("SHA256SUMS", []byte(sums.String()))
	rawFeed, _ := json.Marshal(feed)
	add("latest.json", rawFeed)
	return transport, release
}

func setFestivalRelease(t *testing.T, transport releaseTransport, release publicRelease) {
	t.Helper()
	setFestivalReleaseFor(t, transport, release)
}

func setFestivalReleaseFor(t *testing.T, transport releaseTransport, release publicRelease) {
	t.Helper()
	raw, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	transport["https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/"+release.Tag] = raw
}

func festivalCatalog(t *testing.T) string {
	return catalogFixture(t)
}

func TestPrepareFestivalAppWritesD003EntryAndVerifiedManifest(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixture(t)
	setFestivalRelease(t, transport, release)
	client := &http.Client{Transport: transport}
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", client); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, festivalAppManifest)
	manifest, err := readJSONDocument(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	rel := manifest["releases"].([]any)[0].(map[string]any)
	artifacts := rel["artifacts"].([]any)
	if len(artifacts) != 5 {
		t.Fatalf("want 5 artifacts, got %d", len(artifacts))
	}
	for i, value := range artifacts {
		artifact := value.(map[string]any)
		target := festivalAppTargets[i]
		digest := sha256.Sum256(transport[artifact["url"].(string)])
		if artifact["kind"] != target.Kind || artifact["os"] != target.OS || artifact["arch"] != target.Arch {
			t.Fatalf("artifact %d identity wrong: %v", i, artifact)
		}
		if artifact["sha256"] != fmt.Sprintf("%x", digest) || artifact["updater_signature"] != "signature for "+artifact["filename"].(string) {
			t.Fatalf("artifact %d not derived from published bytes: %v", i, artifact)
		}
		if strings.HasSuffix(artifact["filename"].(string), ".dmg") {
			t.Fatal("dmg must be ignored")
		}
	}
	entries := rel["install"].(map[string]any)["entries"].([]any)
	if entries[0].(map[string]any)["kind"] != "desktop_app" {
		t.Fatalf("install entry: %v", entries)
	}
	catalog, _ := readJSONDocument(filepath.Join(root, "obey-marketplace.json"))
	packages := catalog["packages"].([]any)
	if len(packages) != 2 || packages[0].(map[string]any)["custom"] != "preserve me" {
		t.Fatalf("catalog not preserved: %v", packages)
	}
	paths := []string{manifestPath, filepath.Join(root, "obey-marketplace.json"), filepath.Join(root, "index.json")}
	snapshots := map[string][]byte{}
	for _, p := range paths {
		snapshots[p], _ = os.ReadFile(p)
	}
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", client); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		current, _ := os.ReadFile(p)
		if !bytes.Equal(snapshots[p], current) {
			t.Fatalf("idempotent run changed %s", p)
		}
	}
	if output := os.Getenv("METADATA_TEST_EXPORT"); output != "" {
		for _, p := range paths {
			rel, _ := filepath.Rel(root, p)
			dest := filepath.Join(output, rel)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dest, snapshots[p], 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPrepareFestivalAppRepairsPartialPreparation(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixture(t)
	setFestivalRelease(t, transport, release)
	client := &http.Client{Transport: transport}
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", client); err != nil {
		t.Fatal(err)
	}
	writeLayout(t, root, map[string]string{"index.json": `{"source":"official-obey","updatedAt":"2026-01-01T00:00:00Z","packages":[{"id":"obedience-corp/camp-buzz","channels":["stable"]}]}`})
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", client); err != nil {
		t.Fatal(err)
	}
	index, err := readJSONDocument(filepath.Join(root, "index.json"))
	if err != nil || len(index["packages"].([]any)) != 2 {
		t.Fatalf("not repaired: %v %v", index, err)
	}
}

func TestPrepareFestivalAppRejectsMutatedPublishedVersion(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixture(t)
	setFestivalRelease(t, transport, release)
	client := &http.Client{Transport: transport}
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", client); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, festivalAppManifest)
	before, _ := os.ReadFile(manifestPath)
	name := "Festival.App_0.1.8_x64-setup.exe"
	data := []byte("different bytes")
	transport[campAppReleaseRoot+"app-v0.1.8/"+name] = data
	digest := sha256.Sum256(data)
	sums := string(transport[campAppReleaseRoot+"app-v0.1.8/SHA256SUMS"])
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(sums), "\n") {
		if strings.HasSuffix(line, name) {
			line = fmt.Sprintf("%x  %s", digest, name)
		}
		out = append(out, line)
	}
	transport[campAppReleaseRoot+"app-v0.1.8/SHA256SUMS"] = []byte(strings.Join(out, "\n") + "\n")
	err := prepareFestivalApp(context.Background(), root, "0.1.8", client)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("mutation accepted: %v", err)
	}
	after, _ := os.ReadFile(manifestPath)
	if !bytes.Equal(before, after) {
		t.Fatal("mutation rewrote manifest")
	}
}

func TestPrepareFestivalAppOversizeArtifactDoesNotWrite(t *testing.T) {
	old := publicArtifactLimit
	publicArtifactLimit = 8
	t.Cleanup(func() { publicArtifactLimit = old })
	root := festivalCatalog(t)
	transport, release := festivalAppFixture(t)
	setFestivalRelease(t, transport, release)
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", &http.Client{Transport: transport}); err == nil {
		t.Fatal("oversize artifact accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "packages")); !os.IsNotExist(err) {
		t.Fatal("oversize wrote metadata")
	}
}

func TestPrepareFestivalAppCancellationDoesNotWrite(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixture(t)
	setFestivalRelease(t, transport, release)
	client := &http.Client{Transport: transport}
	for _, v := range []string{"v0.1.8", "0.1.8-rc.1", "app-v0.1.8", ""} {
		if err := prepareFestivalApp(context.Background(), root, v, client); err == nil {
			t.Fatalf("version %q accepted", v)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareFestivalApp(ctx, root, "0.1.8", client); err == nil {
		t.Fatal("canceled accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "packages")); !os.IsNotExist(err) {
		t.Fatal("wrote metadata")
	}
}

func TestPrepareFestivalAppAppendsNewVersionAndKeepsHistory(t *testing.T) {
	root := festivalCatalog(t)
	client := func(v string) *http.Client {
		transport, release := festivalAppFixtureFor(t, v)
		setFestivalReleaseFor(t, transport, release)
		return &http.Client{Transport: transport}
	}
	if err := prepareFestivalApp(context.Background(), root, "0.2.0", client("0.2.0")); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, festivalAppManifest)
	first, _ := readJSONDocument(manifestPath)
	firstBytes, _ := json.Marshal(first["releases"].([]any)[0])
	if err := prepareFestivalApp(context.Background(), root, "0.2.1", client("0.2.1")); err != nil {
		t.Fatal(err)
	}
	second, _ := readJSONDocument(manifestPath)
	releases := second["releases"].([]any)
	if len(releases) != 2 || releases[0].(map[string]any)["version"] != "0.2.0" || releases[1].(map[string]any)["version"] != "0.2.1" {
		t.Fatalf("history lost: %v", releases)
	}
	keptBytes, _ := json.Marshal(releases[0])
	if !bytes.Equal(firstBytes, keptBytes) {
		t.Fatal("0.2.0 release changed")
	}
}

type delayedTransport struct {
	base  releaseTransport
	delay time.Duration
	slow  func(string) bool
}

func (d delayedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if d.slow(req.URL.String()) {
		select {
		case <-time.After(d.delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return d.base.RoundTrip(req)
}

func TestPrepareFestivalAppArtifactStreamingIgnoresClientTotalTimeout(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixture(t)
	setFestivalRelease(t, transport, release)
	slow := func(u string) bool {
		return strings.Contains(u, "/Festival.App_0.1.8_") && !strings.HasSuffix(u, ".sig")
	}
	client := &http.Client{Transport: delayedTransport{transport, 100 * time.Millisecond, slow}, Timeout: 20 * time.Millisecond}
	if err := prepareFestivalApp(context.Background(), root, "0.1.8", client); err != nil {
		t.Fatalf("streaming should ignore the total timeout: %v", err)
	}
	url := campAppReleaseRoot + "app-v0.1.8/Festival.App_0.1.8_amd64.deb"
	digest := sha256.Sum256(transport[url])
	if err := verifyPublicArtifact(context.Background(), client, url, fmt.Sprintf("%x", digest)); err == nil {
		t.Fatal("control: original client should time out")
	}
}
