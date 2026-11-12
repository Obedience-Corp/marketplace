package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const releaseListURL = "https://api.github.com/repos/Obedience-Corp/obey-releases/releases?per_page=100&page=1"

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	fn()
	writer.Close()
	os.Stdout = original
	out, _ := io.ReadAll(reader)
	return string(out)
}

func setReleaseList(transport releaseTransport, releases []publicRelease) {
	transport[releaseListURL], _ = json.Marshal(releases)
}

func catalogSnapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range []string{"obey-marketplace.json", "index.json", festivalAppManifest} {
		out[name], _ = os.ReadFile(filepath.Join(root, name))
	}
	return out
}

func TestFestivalAppRouteSelectsOnlyPublishedAppV(t *testing.T) {
	now := time.Now()
	transport := releaseTransport{}
	setReleaseList(transport, []publicRelease{
		{Tag: "app-test-" + strings.Repeat("a", 40), PublishedAt: now, Prerelease: true},
		{Tag: "app-stable", PublishedAt: now},
		{Tag: "app-v0.3.0", PublishedAt: now, Draft: true},
		{Tag: "app-v0.2.5", PublishedAt: now, Prerelease: true},
		{Tag: "app-v0.2.0-rc.1", PublishedAt: now},
		{Tag: "app-v0.2.1", PublishedAt: now},
		{Tag: "camp-app-v9.0.0", PublishedAt: now},
	})
	client := &http.Client{Transport: transport}
	got, err := latestPublishedVersion(context.Background(), client, releaseRoutes["festival-app"])
	if err != nil || got != "0.2.1" {
		t.Fatalf("latest = %q, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := latestPublishedVersion(ctx, client, releaseRoutes["festival-app"]); err == nil {
		t.Fatal("cancelled lookup succeeded")
	}
}

func TestReconcileFestivalAppWithNoStableReleaseIsCurrent(t *testing.T) {
	root := festivalCatalog(t)
	transport := releaseTransport{}
	setReleaseList(transport, []publicRelease{{Tag: "app-test-" + strings.Repeat("b", 40), PublishedAt: time.Now(), Prerelease: true}})
	before := catalogSnapshot(t, root)
	var err error
	out := captureStdout(t, func() {
		err = reconcileRelease(context.Background(), root, "festival-app", "latest", &http.Client{Transport: transport})
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "obedience-corp/festival-app: no published stable app-v release yet; catalog unchanged\n" {
		t.Fatalf("stdout = %q", out)
	}
	for name, data := range catalogSnapshot(t, root) {
		if !bytes.Equal(before[name], data) {
			t.Fatalf("%s changed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "packages/obedience-corp/festival-app")); !os.IsNotExist(err) {
		t.Fatal("manifest dir written")
	}
}

func TestReconcileFestivalAppNoneYetFailsWhenCatalogHasApp(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixtureFor(t, "0.2.0")
	setFestivalReleaseFor(t, transport, release)
	if err := prepareFestivalApp(context.Background(), root, "0.2.0", &http.Client{Transport: transport}); err != nil {
		t.Fatal(err)
	}
	empty := releaseTransport{}
	setReleaseList(empty, nil)
	err := reconcileRelease(context.Background(), root, "festival-app", "latest", &http.Client{Transport: empty})
	if !errors.Is(err, errNoPublishedRelease) {
		t.Fatalf("want errNoPublishedRelease, got %v", err)
	}
}

func TestReconcileFestivalAppExplicitMissingVersionFails(t *testing.T) {
	root := festivalCatalog(t)
	before := catalogSnapshot(t, root)
	if err := reconcileRelease(context.Background(), root, "festival-app", "0.2.0", &http.Client{Transport: releaseTransport{}}); err == nil {
		t.Fatal("missing explicit version accepted")
	}
	for name, data := range catalogSnapshot(t, root) {
		if !bytes.Equal(before[name], data) {
			t.Fatalf("%s changed", name)
		}
	}
}

func TestReconcileCampAppWithNoStableReleaseStillFails(t *testing.T) {
	root := festivalCatalog(t)
	transport := releaseTransport{}
	setReleaseList(transport, nil)
	err := reconcileRelease(context.Background(), root, "camp-app", "latest", &http.Client{Transport: transport})
	if err == nil || !strings.Contains(err.Error(), "no published stable camp-app-v release") {
		t.Fatalf("camp-app none-yet should fail: %v", err)
	}
}

func TestReconcileFestivalAppPreparesLatestThenSkipsDuplicate(t *testing.T) {
	root := festivalCatalog(t)
	transport, release := festivalAppFixtureFor(t, "0.2.0")
	setFestivalReleaseFor(t, transport, release)
	setReleaseList(transport, []publicRelease{release})
	if err := reconcileRelease(context.Background(), root, "festival-app", "latest", &http.Client{Transport: transport}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, festivalAppManifest)); err != nil {
		t.Fatal(err)
	}
	before := catalogSnapshot(t, root)
	var err error
	out := captureStdout(t, func() {
		err = reconcileRelease(context.Background(), root, "festival-app", "0.2.0", &http.Client{Transport: releaseTransport{}})
	})
	if err != nil || !strings.Contains(out, "already includes or supersedes") {
		t.Fatalf("duplicate: %v %q", err, out)
	}
	for name, data := range catalogSnapshot(t, root) {
		if !bytes.Equal(before[name], data) {
			t.Fatalf("%s changed", name)
		}
	}
}
