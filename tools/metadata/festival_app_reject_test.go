package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareFestivalAppRejectsInvalidReleasesWithoutWrites(t *testing.T) {
	base := campAppReleaseRoot + "app-v0.1.8/"
	setFeed := func(transport releaseTransport, edit func(*updaterFeed)) {
		var feed updaterFeed
		_ = json.Unmarshal(transport[base+"latest.json"], &feed)
		edit(&feed)
		transport[base+"latest.json"], _ = json.Marshal(feed)
	}
	dropAsset := func(release *publicRelease, name string) {
		var kept []releaseAsset
		for _, a := range release.Assets {
			if a.Name != name {
				kept = append(kept, a)
			}
		}
		release.Assets = kept
	}
	tests := []struct {
		name   string
		mutate func(releaseTransport, *publicRelease)
	}{
		{"missing release", func(tr releaseTransport, _ *publicRelease) { delete(tr, festivalAppAPI) }},
		{"draft", func(_ releaseTransport, r *publicRelease) { r.Draft = true }},
		{"prerelease", func(_ releaseTransport, r *publicRelease) { r.Prerelease = true }},
		{"wrong tag", func(_ releaseTransport, r *publicRelease) { r.Tag = "app-test-1" }},
		{"unpublished", func(_ releaseTransport, r *publicRelease) { r.PublishedAt = time.Time{} }},
		{"wrong asset host", func(_ releaseTransport, r *publicRelease) { r.Assets[0].URL = "https://untrusted.invalid/x" }},
		{"missing sums", func(_ releaseTransport, r *publicRelease) { dropAsset(r, "SHA256SUMS") }},
		{"missing feed", func(_ releaseTransport, r *publicRelease) { dropAsset(r, "latest.json") }},
		{"missing target", func(_ releaseTransport, r *publicRelease) { dropAsset(r, "Festival.App_0.1.8_amd64.deb") }},
		{"missing sig asset", func(_ releaseTransport, r *publicRelease) { dropAsset(r, "Festival.App_0.1.8_amd64.deb.sig") }},
		{"feed version mismatch", func(tr releaseTransport, _ *publicRelease) { setFeed(tr, func(f *updaterFeed) { f.Version = "0.1.7" }) }},
		{"feed signature mismatch", func(tr releaseTransport, _ *publicRelease) {
			setFeed(tr, func(f *updaterFeed) {
				e := f.Platforms["windows-x86_64"]
				e.Signature = "forged"
				f.Platforms["windows-x86_64"] = e
			})
		}},
		{"feed url mismatch", func(tr releaseTransport, _ *publicRelease) {
			setFeed(tr, func(f *updaterFeed) {
				e := f.Platforms["linux-x86_64"]
				e.URL = "https://untrusted.invalid/x"
				f.Platforms["linux-x86_64"] = e
			})
		}},
		{"feed platform missing", func(tr releaseTransport, _ *publicRelease) {
			setFeed(tr, func(f *updaterFeed) { delete(f.Platforms, "darwin-x86_64") })
		}},
		{"checksum mismatch", func(tr releaseTransport, _ *publicRelease) {
			tr[base+"Festival.App_0.1.8_amd64.deb"] = []byte("corrupt")
		}},
		{"duplicate checksums", func(tr releaseTransport, _ *publicRelease) {
			tr[base+"SHA256SUMS"] = append(tr[base+"SHA256SUMS"], tr[base+"SHA256SUMS"]...)
		}},
		{"missing checksum line", func(tr releaseTransport, _ *publicRelease) {
			var kept []string
			for _, l := range strings.Split(strings.TrimSpace(string(tr[base+"SHA256SUMS"])), "\n") {
				if !strings.HasSuffix(l, "_amd64.AppImage") {
					kept = append(kept, l)
				}
			}
			tr[base+"SHA256SUMS"] = []byte(strings.Join(kept, "\n") + "\n")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := festivalCatalog(t)
			transport, release := festivalAppFixture(t)
			test.mutate(transport, &release)
			if test.name != "missing release" {
				setFestivalRelease(t, transport, release)
			}
			path := filepath.Join(root, "obey-marketplace.json")
			before, _ := os.ReadFile(path)
			if err := prepareFestivalApp(context.Background(), root, "0.1.8", &http.Client{Transport: transport}); err == nil {
				t.Fatal("invalid release accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed validation mutated the catalog")
			}
			if _, err := os.Stat(filepath.Join(root, "packages")); !os.IsNotExist(err) {
				t.Fatal("failed validation wrote a manifest")
			}
		})
	}
}
