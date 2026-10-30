package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type releaseRoute struct {
	repo, prefix, packageID string
}

// Only established static packages need catalog writes. Dynamic release_source
// packages continue discovering releases through their signed routing contract.
var releaseRoutes = map[string]releaseRoute{
	"camp-app": {"Obedience-Corp/obey-releases", "camp-app-v", campAppID},
	"festival": {"Obedience-Corp/festival", "v", "obedience-corp/festival"},
}

func reconcileRelease(ctx context.Context, root, name, version string, client *http.Client) error {
	route, ok := releaseRoutes[name]
	if !ok {
		return fmt.Errorf("no registered static release route for %q", name)
	}
	if version == "latest" {
		var err error
		version, err = latestPublishedVersion(ctx, client, route)
		if err != nil {
			return err
		}
	}
	if _, err := versionParts(version); err != nil {
		return err
	}
	current, err := catalogVersion(root, route.packageID)
	if err != nil {
		return err
	}
	if current != "" {
		cmp, err := compareVersions(version, current)
		if err != nil {
			return err
		}
		if cmp <= 0 {
			fmt.Printf("%s: catalog %s already includes or supersedes %s\n", route.packageID, current, version)
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "camp-app" {
		return prepareCampApp(ctx, root, version, client)
	}
	return prepareFestival(ctx, root, version, client)
}

func latestPublishedVersion(ctx context.Context, client *http.Client, route releaseRoute) (string, error) {
	best := ""
	for page := 1; ; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", route.repo, page)
		raw, err := publicBytes(ctx, client, url, 32<<20)
		if err != nil {
			return "", err
		}
		var releases []publicRelease
		if err := json.Unmarshal(raw, &releases); err != nil {
			return "", fmt.Errorf("decode published releases: %w", err)
		}
		for _, release := range releases {
			if release.Draft || release.Prerelease || release.PublishedAt.IsZero() || !strings.HasPrefix(release.Tag, route.prefix) {
				continue
			}
			version := strings.TrimPrefix(release.Tag, route.prefix)
			if _, err := versionParts(version); err != nil {
				continue
			}
			cmp, _ := compareVersions(version, best)
			if best == "" || cmp > 0 {
				best = version
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	if best == "" {
		return "", fmt.Errorf("no published stable %s release in %s", route.prefix, route.repo)
	}
	return best, nil
}

func versionParts(version string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(version, ".")
	if len(parts) != 3 || !stableVersion.MatchString(version) {
		return result, fmt.Errorf("invalid stable version %q", version)
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || (len(part) > 1 && part[0] == '0') {
			return result, fmt.Errorf("invalid stable version %q", version)
		}
		result[i] = n
	}
	return result, nil
}

func compareVersions(a, b string) (int, error) {
	x, err := versionParts(a)
	if err != nil {
		return 0, err
	}
	y, err := versionParts(b)
	if err != nil {
		return 0, err
	}
	for i := range x {
		if x[i] < y[i] {
			return -1, nil
		}
		if x[i] > y[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func catalogVersion(root, id string) (string, error) {
	doc, err := readJSONDocument(filepath.Join(root, "packages", filepath.FromSlash(id), "obey-package.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if doc["id"] != id {
		return "", fmt.Errorf("unexpected package identity for %s", id)
	}
	releases, ok := doc["releases"].([]any)
	if !ok {
		return "", fmt.Errorf("invalid release history for %s", id)
	}
	best := ""
	for _, value := range releases {
		release, ok := value.(map[string]any)
		if !ok {
			return "", fmt.Errorf("invalid release in %s", id)
		}
		if release["channel"] != "stable" {
			continue
		}
		version, _ := release["version"].(string)
		if _, err := versionParts(version); err != nil {
			return "", err
		}
		cmp, _ := compareVersions(version, best)
		if best == "" || cmp > 0 {
			best = version
		}
	}
	return best, nil
}

// Suite metadata is emitted by the existing release operator and published with
// the suite. This consumer verifies its concrete artifacts before catalog writes.
func prepareFestival(ctx context.Context, root, version string, client *http.Client) error {
	tag := "v" + version
	base := "https://github.com/Obedience-Corp/festival/releases/download/" + tag + "/"
	raw, err := publicBytes(ctx, client, "https://api.github.com/repos/Obedience-Corp/festival/releases/tags/"+tag, 1<<20)
	if err != nil {
		return err
	}
	var published publicRelease
	if err := json.Unmarshal(raw, &published); err != nil {
		return err
	}
	if published.Tag != tag || published.Draft || published.Prerelease || published.PublishedAt.IsZero() {
		return fmt.Errorf("%s is not a published stable suite release", tag)
	}
	assets := map[string]string{}
	for _, asset := range published.Assets {
		if asset.URL != base+asset.Name || filepath.Base(asset.Name) != asset.Name || assets[asset.Name] != "" {
			return fmt.Errorf("unexpected suite asset identity %q", asset.Name)
		}
		assets[asset.Name] = asset.URL
	}
	if assets["obey-package.json"] == "" || assets["checksums.txt"] == "" {
		return fmt.Errorf("%s has no published marketplace manifest/checksums; retry suite post-publish", tag)
	}
	raw, err = publicBytes(ctx, client, assets["obey-package.json"], 1<<20)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	releases, ok := doc["releases"].([]any)
	if !ok || len(releases) != 1 || doc["id"] != "obedience-corp/festival" || doc["class"] != "product" {
		return fmt.Errorf("unexpected published suite manifest identity")
	}
	release, ok := releases[0].(map[string]any)
	if !ok || release["version"] != version || release["channel"] != "stable" || release["published_at"] != published.PublishedAt.UTC().Format("2006-01-02T15:04:05Z") {
		return fmt.Errorf("published suite release identity mismatch")
	}
	raw, err = publicBytes(ctx, client, assets["checksums.txt"], 1<<20)
	if err != nil {
		return err
	}
	sums, err := parseReleaseChecksums(raw)
	if err != nil {
		return err
	}
	artifacts, ok := release["artifacts"].([]any)
	if !ok || len(artifacts) == 0 {
		return fmt.Errorf("published suite has no artifacts")
	}
	seen := map[string]bool{}
	for _, value := range artifacts {
		artifact, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid suite artifact")
		}
		name, _ := artifact["filename"].(string)
		url, _ := artifact["url"].(string)
		digest, _ := artifact["sha256"].(string)
		if name == "" || seen[name] || assets[name] != url || url != base+name || sums[name] != digest || digest == "" {
			return fmt.Errorf("suite artifact/checksum identity mismatch: %q", name)
		}
		seen[name] = true
		if err := verifyPublicArtifact(ctx, client, url, digest); err != nil {
			return err
		}
	}
	for _, target := range []string{"macOS-all.tar.gz", "linux-arm64.tar.gz", "linux-x86_64.tar.gz", "windows-x86_64.zip"} {
		if !seen["festival-"+version+"-"+target] {
			return fmt.Errorf("missing supported suite target %s", target)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manifestPath := filepath.Join(root, "packages", "obedience-corp", "festival", "obey-package.json")
	previous, err := readJSONDocument(manifestPath)
	if err != nil {
		return err
	}
	if previous["id"] != doc["id"] {
		return fmt.Errorf("existing suite manifest identity mismatch")
	}
	history, ok := previous["releases"].([]any)
	if !ok {
		return fmt.Errorf("invalid existing suite history")
	}
	for _, value := range history {
		old, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid existing suite release")
		}
		if old["version"] == version && old["channel"] == "stable" {
			before, _ := json.Marshal(old)
			after, _ := json.Marshal(release)
			if !bytes.Equal(before, after) {
				return fmt.Errorf("immutable suite version has different metadata")
			}
			return nil
		}
	}
	previous["releases"] = append(history, release)
	raw, err = json.Marshal(previous)
	if err != nil {
		return err
	}
	raw, err = canonicalize(raw)
	if err != nil {
		return err
	}
	if err := writeAtomic(manifestPath, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("prepared obedience-corp/festival %s from verified public artifacts\n", version)
	return nil
}

func verifyPublicArtifact(ctx context.Context, client *http.Client, url, digest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	hash := sha256.New()
	const limit = 512 << 20
	n, err := io.Copy(hash, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("artifact size/checksum mismatch for %s", url)
	}
	return nil
}
