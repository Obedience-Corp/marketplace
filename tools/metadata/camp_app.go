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
	"regexp"
	"strings"
	"time"
)

const campAppID = "obedience-corp/camp-app"
const campAppReleaseRoot = "https://github.com/Obedience-Corp/obey-releases/releases/download/"

var stableVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var sourceCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

type publicRelease struct {
	Tag         string         `json:"tag_name"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	PublishedAt time.Time      `json:"published_at"`
	Assets      []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func publicReleaseClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return fmt.Errorf("refuse unsafe public release redirect")
		}
		return nil
	}}
}

// prepareCampApp writes only after all actual public assets validate. It does not
// publish, sign, infer a producer namespace, or change unrelated catalog entries.
func prepareCampApp(ctx context.Context, root, version string, client *http.Client) error {
	if !stableVersion.MatchString(version) {
		return fmt.Errorf("Camp App version must be an unprefixed stable X.Y.Z")
	}
	tag := "camp-app-v" + version
	api := "https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/" + tag
	raw, err := publicBytes(ctx, client, api, 1<<20)
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
	checksumURL, ok := assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("published checksums.txt is missing; catalog unchanged")
	}
	checksumsRaw, err := publicBytes(ctx, client, checksumURL, 1<<20)
	if err != nil {
		return err
	}
	checksums, err := parseReleaseChecksums(checksumsRaw)
	if err != nil {
		return err
	}
	artifacts := make([]any, 0, 5)
	commit := ""
	for _, target := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "windows-amd64", "linux-arm64"} {
		name := "camp-app-" + version + "-" + target + ".tar.gz"
		url, exists := assets[name]
		if !exists && target == "linux-arm64" { // Optional producer target, never fabricated.
			continue
		}
		if !exists || checksums[name] == "" {
			return fmt.Errorf("published artifact/checksum %s is missing; catalog unchanged", name)
		}
		payload, err := publicBytes(ctx, client, url, 64<<20)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(payload)
		if hex.EncodeToString(digest[:]) != checksums[name] {
			return fmt.Errorf("checksum mismatch for %s; catalog unchanged", name)
		}
		identity, err := campAppArchiveIdentity(payload, version, target)
		if err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
		if commit != "" && identity != commit {
			return fmt.Errorf("producer revisions differ across archives; catalog unchanged")
		}
		commit = identity
		platform := strings.Split(target, "-")
		binary := "camp-app"
		if platform[0] == "windows" {
			binary += ".exe"
		}
		artifacts = append(artifacts, map[string]any{
			"kind": "tar.gz", "os": platform[0], "arch": platform[1], "url": url,
			"filename": name, "sha256": checksums[name], "binaries": []string{binary},
		})
	}
	releaseDocument := map[string]any{
		"version": version, "channel": "stable", "published_at": release.PublishedAt.UTC().Format(time.RFC3339),
		"compatibility": map[string]any{"os": []string{"darwin", "linux", "windows"}, "arch": []string{"amd64", "arm64"}},
		"dependencies":  []any{}, "artifacts": artifacts,
		"install": map[string]any{"entries": []any{map[string]any{"kind": "binary", "source": "camp-app", "executable_name": "camp-app"}}},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeCampAppDocuments(root, releaseDocument, commit)
}

func publicBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("public asset exceeds size budget: %s", url)
	}
	return data, nil
}

func parseReleaseChecksums(raw []byte) (map[string]string, error) {
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || len(parts[0]) != 64 || filepath.Base(parts[1]) != parts[1] {
			return nil, fmt.Errorf("invalid published checksum line")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, fmt.Errorf("invalid checksum: %w", err)
		}
		if _, duplicate := result[parts[1]]; duplicate {
			return nil, fmt.Errorf("duplicate published checksum for %s", parts[1])
		}
		result[parts[1]] = strings.ToLower(parts[0])
	}
	return result, nil
}

func campAppArchiveIdentity(raw []byte, version, target string) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 64<<20))
	seen := make(map[string]bool)
	binary := "camp-app"
	if strings.HasPrefix(target, "windows-") {
		binary += ".exe"
	}
	allowed := map[string]bool{binary: true, "release.json": true, "README.md": true, "LICENSE": true}
	var identity struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Target  string `json:"target"`
	}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Typeflag != tar.TypeReg || !allowed[header.Name] || seen[header.Name] || header.Size <= 0 {
			return "", fmt.Errorf("unexpected archive member %q", header.Name)
		}
		seen[header.Name] = true
		if header.Name == "release.json" {
			if header.Size > 4096 {
				return "", fmt.Errorf("release identity is oversized")
			}
			identityRaw, err := io.ReadAll(reader)
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(identityRaw, &identity); err != nil {
				return "", err
			}
		}
	}
	if len(seen) != len(allowed) || identity.Version != version || identity.Target != target || !sourceCommit.MatchString(identity.Commit) {
		return "", fmt.Errorf("incomplete or mismatched archive identity")
	}
	return identity.Commit, nil
}

func writeCampAppDocuments(root string, release map[string]any, commit string) error {
	manifestPath := filepath.Join(root, "packages", "obedience-corp", "camp-app", "obey-package.json")
	manifest := map[string]any{
		"schema_version": 1, "id": campAppID, "class": "plugin", "display_name": "Camp App",
		"summary": "Festival App terminal and agent control companion.", "provides_binaries": []string{"camp-app"},
		"targets":  []any{map[string]any{"package": "obedience-corp/festival", "runtime": "camp-cli"}},
		"releases": []any{release},
	}
	manifestChanged := true
	if raw, err := os.ReadFile(manifestPath); err == nil {
		var previous map[string]any
		if err := json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("read existing Camp App manifest: %w", err)
		}
		releases, ok := previous["releases"].([]any)
		if !ok || previous["id"] != campAppID {
			return fmt.Errorf("existing Camp App manifest has an unexpected identity")
		}
		knownRelease := false
		for _, item := range releases {
			entry, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid existing release")
			}
			if entry["version"] == release["version"] {
				oldBytes, _ := json.Marshal(entry)
				newBytes, _ := json.Marshal(release)
				if !bytes.Equal(oldBytes, newBytes) {
					return fmt.Errorf("immutable Camp App version already has different metadata")
				}
				knownRelease = true
				break
			}
		}
		if knownRelease {
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
	entry := map[string]any{"id": campAppID, "class": "plugin", "display_name": "Camp App", "summary": "Festival App terminal and agent control companion.",
		"channels": []string{"stable"}, "host_runtimes": []string{"camp-cli"},
		"targets":       []any{map[string]any{"package": "obedience-corp/festival", "runtime": "camp-cli"}},
		"manifest_path": "packages/obedience-corp/camp-app/obey-package.json"}
	marketplaceChanged, err := upsertCampApp(marketplace, entry)
	if err != nil {
		return err
	}
	indexChanged, err := upsertCampApp(index, map[string]any{"id": campAppID, "channels": []string{"stable"}})
	if err != nil {
		return err
	}
	if manifestChanged || marketplaceChanged || indexChanged {
		// Publication retries must reproduce the exact prepared payload while a
		// signed PR is pending. Preserve a newer catalog timestamp for old events.
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
	fmt.Printf("prepared %s %s at source %s from public assets; run the existing signing/review workflow\n", campAppID, release["version"], commit)
	return nil
}

func readJSONDocument(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func upsertCampApp(document, entry map[string]any) (bool, error) {
	packages, ok := document["packages"].([]any)
	if !ok {
		return false, fmt.Errorf("catalog packages is not an array")
	}
	for i, item := range packages {
		current, ok := item.(map[string]any)
		if !ok {
			return false, fmt.Errorf("catalog entry is not an object")
		}
		if current["id"] != campAppID {
			continue
		}
		before, err := json.Marshal(current)
		if err != nil {
			return false, err
		}
		merged := make(map[string]any, len(current)+len(entry))
		for key, value := range current {
			merged[key] = value
		}
		// Only this producer switches to the established static manifest.
		delete(merged, "release_source")
		for key, value := range entry {
			merged[key] = value
		}
		if channels, ok := current["channels"].([]any); ok {
			found := false
			for _, channel := range channels {
				found = found || channel == "stable"
			}
			if !found {
				channels = append(channels, "stable")
			}
			merged["channels"] = channels
		}
		after, err := json.Marshal(merged)
		if err != nil {
			return false, err
		}
		if bytes.Equal(before, after) {
			return false, nil
		}
		packages[i] = merged
		document["packages"] = packages
		return true, nil
	}
	document["packages"] = append(packages, entry)
	return true, nil
}
