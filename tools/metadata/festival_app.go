package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	festivalAppID          = "obedience-corp/festival-app"
	festivalAppName        = "Festival App"
	festivalAppSummary     = "The desktop home for Festival: open a camp, save a plan and follow its progress."
	festivalAppBundleID    = "com.obediencecorp.festival-app"
	festivalAppManifest    = "packages/obedience-corp/festival-app/obey-package.json"
	festivalAppArtifactTTL = 15 * time.Minute // per artifact; replaces the client's 60 s total timeout
)

type festivalAppTarget struct{ Suffix, OS, Arch, Kind, Platform string }

// Order is the manifest order. .dmg assets are ignored.
var festivalAppTargets = []festivalAppTarget{
	{"_aarch64.app.tar.gz", "darwin", "arm64", "app-bundle", "darwin-aarch64"},
	{"_x64.app.tar.gz", "darwin", "amd64", "app-bundle", "darwin-x86_64"},
	{"_x64-setup.exe", "windows", "amd64", "nsis", "windows-x86_64"},
	{"_amd64.deb", "linux", "amd64", "deb", "linux-x86_64-deb"},
	{"_amd64.AppImage", "linux", "amd64", "appimage", "linux-x86_64"},
}

type updaterFeed struct {
	Version   string `json:"version"`
	Platforms map[string]struct {
		Signature string `json:"signature"`
		URL       string `json:"url"`
	} `json:"platforms"`
}

// prepareFestivalApp writes only after every public asset and updater signature
// validates. It neither signs nor publishes.
func prepareFestivalApp(ctx context.Context, root, version string, client *http.Client) error {
	if !stableVersion.MatchString(version) {
		return fmt.Errorf("Festival App version must be an unprefixed stable X.Y.Z")
	}
	tag := "app-v" + version
	raw, err := publicBytes(ctx, client, "https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/"+tag, 1<<20)
	if err != nil {
		return fmt.Errorf("read published %s first; catalog unchanged: %w", tag, err)
	}
	var release publicRelease
	if err := json.Unmarshal(raw, &release); err != nil {
		return fmt.Errorf("decode public release: %w", err)
	}
	if release.Tag != tag || release.Draft || release.Prerelease || release.PublishedAt.IsZero() {
		return fmt.Errorf("%s is not the published stable release; catalog unchanged", tag)
	}
	assets := make(map[string]string)
	for _, asset := range release.Assets {
		if asset.URL != campAppReleaseRoot+tag+"/"+asset.Name || filepath.Base(asset.Name) != asset.Name {
			return fmt.Errorf("unexpected public asset identity %q; catalog unchanged", asset.Name)
		}
		if _, duplicate := assets[asset.Name]; duplicate {
			return fmt.Errorf("duplicate release asset %q", asset.Name)
		}
		assets[asset.Name] = asset.URL
	}
	sumsURL, ok := assets["SHA256SUMS"]
	if !ok {
		return fmt.Errorf("published SHA256SUMS is missing; catalog unchanged")
	}
	sumsRaw, err := publicBytes(ctx, client, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	sums, err := parseReleaseChecksums(sumsRaw)
	if err != nil {
		return err
	}
	feedURL, ok := assets["latest.json"]
	if !ok {
		return fmt.Errorf("published latest.json is missing; catalog unchanged")
	}
	feedRaw, err := publicBytes(ctx, client, feedURL, 1<<20)
	if err != nil {
		return err
	}
	var feed updaterFeed
	if err := json.Unmarshal(feedRaw, &feed); err != nil {
		return fmt.Errorf("decode latest.json: %w", err)
	}
	if feed.Version != version {
		return fmt.Errorf("latest.json version %q does not match %s; catalog unchanged", feed.Version, version)
	}
	artifacts, err := festivalAppArtifacts(ctx, client, tag, version, assets, sums, feed)
	if err != nil {
		return err
	}
	releaseDocument := map[string]any{
		"version": version, "channel": "stable", "published_at": release.PublishedAt.UTC().Format(time.RFC3339),
		"compatibility": map[string]any{"os": []string{"darwin", "linux", "windows"}, "arch": []string{"amd64", "arm64"}},
		"dependencies":  []any{}, "artifacts": artifacts,
		"install": map[string]any{"entries": []any{map[string]any{
			"kind": "desktop_app", "source": "festival-app", "bundle_id": festivalAppBundleID, "product_name": festivalAppName,
		}}},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeFestivalAppDocuments(root, releaseDocument)
}

// festivalAppArtifacts checks every cheap fact for all five targets first, then
// streams each artifact once.
func festivalAppArtifacts(ctx context.Context, client *http.Client, tag, version string, assets, sums map[string]string, feed updaterFeed) ([]any, error) {
	type checked struct {
		target         festivalAppTarget
		name, url, sig string
	}
	var plan []checked
	for _, t := range festivalAppTargets {
		name := "Festival.App_" + version + t.Suffix
		url, exists := assets[name]
		if !exists || sums[name] == "" {
			return nil, fmt.Errorf("published %s is missing; catalog unchanged", name)
		}
		sigURL, exists := assets[name+".sig"]
		if !exists {
			return nil, fmt.Errorf("published %s.sig is missing; catalog unchanged", name)
		}
		sigRaw, err := publicBytes(ctx, client, sigURL, 4096)
		if err != nil {
			return nil, err
		}
		entry, ok := feed.Platforms[t.Platform]
		if !ok || entry.Signature != string(sigRaw) {
			return nil, fmt.Errorf("updater signature for %s does not match %s.sig; catalog unchanged", t.Platform, name)
		}
		if entry.URL != url {
			return nil, fmt.Errorf("updater signature for %s does not match %s.sig: latest.json url differs; catalog unchanged", t.Platform, name)
		}
		plan = append(plan, checked{t, name, url, string(sigRaw)})
	}
	stream := streamingClient(client)
	out := make([]any, 0, len(plan))
	for _, c := range plan {
		actx, cancel := context.WithTimeout(ctx, festivalAppArtifactTTL)
		err := verifyPublicArtifact(actx, stream, c.url, sums[c.name])
		cancel()
		if err != nil {
			return nil, fmt.Errorf("verify %s: %w; catalog unchanged", c.name, err)
		}
		out = append(out, map[string]any{
			"kind": c.target.Kind, "os": c.target.OS, "arch": c.target.Arch, "url": c.url,
			"sha256": sums[c.name], "filename": c.name, "updater_signature": c.sig,
		})
	}
	return out, nil
}

// streamingClient is a copy with no total timeout. The 60 s total timeout of the
// shared client would cut off a 105 MB artifact on a slow link; each artifact is
// bounded by its own context instead.
func streamingClient(client *http.Client) *http.Client {
	return &http.Client{Transport: client.Transport, CheckRedirect: client.CheckRedirect, Jar: client.Jar}
}

func writeFestivalAppDocuments(root string, release map[string]any) error {
	manifestPath := filepath.Join(root, filepath.FromSlash(festivalAppManifest))
	manifest := map[string]any{
		"schema_version": 1, "id": festivalAppID, "class": "product", "display_name": festivalAppName,
		"summary": festivalAppSummary, "releases": []any{release},
	}
	manifestChanged := true
	if raw, err := os.ReadFile(manifestPath); err == nil {
		var previous map[string]any
		if err := json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("read existing Festival App manifest: %w", err)
		}
		releases, ok := previous["releases"].([]any)
		if !ok || previous["id"] != festivalAppID || previous["class"] != "product" {
			return fmt.Errorf("existing Festival App manifest has an unexpected identity")
		}
		known := false
		for _, item := range releases {
			entry, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid existing release")
			}
			if entry["version"] == release["version"] {
				oldBytes, _ := json.Marshal(entry)
				newBytes, _ := json.Marshal(release)
				if !bytes.Equal(oldBytes, newBytes) {
					return fmt.Errorf("immutable Festival App version already has different metadata")
				}
				known = true
				break
			}
		}
		if known {
			manifestChanged = false
		} else {
			previous["releases"] = append(releases, release)
		}
		manifest = previous
	} else if !os.IsNotExist(err) {
		return err
	}
	marketplacePath, indexPath := filepath.Join(root, "obey-marketplace.json"), filepath.Join(root, "index.json")
	marketplace, err := readJSONDocument(marketplacePath)
	if err != nil {
		return err
	}
	index, err := readJSONDocument(indexPath)
	if err != nil {
		return err
	}
	entry := map[string]any{
		"id": festivalAppID, "display_name": festivalAppName, "class": "product", "summary": festivalAppSummary,
		"channels": []string{"stable"}, "manifest_path": festivalAppManifest,
	}
	marketplaceChanged, err := upsertFestivalAppEntry(marketplace, entry)
	if err != nil {
		return err
	}
	indexChanged, err := upsertFestivalAppEntry(index, map[string]any{"id": festivalAppID, "channels": []string{"stable"}})
	if err != nil {
		return err
	}
	if manifestChanged || marketplaceChanged || indexChanged {
		// Retries must reproduce the exact prepared payload while a signed PR is
		// pending, so a newer catalog timestamp is never moved back.
		published, _ := release["published_at"].(string)
		prior, _ := index["updatedAt"].(string)
		if published > prior {
			index["updatedAt"] = published
		}
	}
	for _, document := range []struct {
		path  string
		value map[string]any
	}{{manifestPath, manifest}, {marketplacePath, marketplace}, {indexPath, index}} {
		raw, err := json.Marshal(document.value)
		if err != nil {
			return err
		}
		raw, err = canonicalize(raw)
		if err != nil {
			return err
		}
		if previous, err := os.ReadFile(document.path); err == nil && bytes.Equal(previous, raw) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(document.path), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(document.path, raw, 0o644); err != nil {
			return fmt.Errorf("write release metadata; inspect uncommitted diff: %w", err)
		}
	}
	fmt.Printf("prepared %s %s from verified public assets; run the existing signing/review workflow\n", festivalAppID, release["version"])
	return nil
}

// upsertFestivalAppEntry appends entry, or replaces the Festival App element with
// exactly entry. It reports whether the document changed.
func upsertFestivalAppEntry(document, entry map[string]any) (bool, error) {
	packages, ok := document["packages"].([]any)
	if !ok {
		return false, fmt.Errorf("catalog packages is not an array")
	}
	for i, item := range packages {
		current, ok := item.(map[string]any)
		if !ok {
			return false, fmt.Errorf("catalog entry is not an object")
		}
		if current["id"] != festivalAppID {
			continue
		}
		before, err := json.Marshal(current)
		if err != nil {
			return false, err
		}
		after, err := json.Marshal(entry)
		if err != nil {
			return false, err
		}
		if bytes.Equal(before, after) {
			return false, nil
		}
		packages[i] = entry
		document["packages"] = packages
		return true, nil
	}
	document["packages"] = append(packages, entry)
	return true, nil
}
