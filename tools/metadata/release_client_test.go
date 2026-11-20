package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type recordingTransport struct{ auth map[string]string }

func (r recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.auth[req.URL.String()] = req.Header.Get("Authorization")
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

func TestPublicReleaseClientAuthenticatesOnlyGitHubAPIReads(t *testing.T) {
	const api = "https://api.github.com/repos/Obedience-Corp/obey-releases/releases/tags/app-v0.1.13"
	const download = "https://github.com/Obedience-Corp/obey-releases/releases/download/app-v0.1.13/SHA256SUMS"
	const plainAPI = "http://api.github.com/repos/Obedience-Corp/obey-releases/releases"
	for _, tc := range []struct {
		name, token string
		want        map[string]string
	}{
		{"with token", "secret", map[string]string{api: "Bearer secret", download: "", plainAPI: ""}},
		{"without token", "", map[string]string{api: "", download: "", plainAPI: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := recordingTransport{auth: map[string]string{}}
			client := newPublicReleaseClient(seen, tc.token)
			for url := range tc.want {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if req.Header.Get("Authorization") != "" {
					t.Fatalf("caller request was mutated for %s", url)
				}
			}
			for url, want := range tc.want {
				if got := seen.auth[url]; got != want {
					t.Fatalf("%s Authorization = %q, want %q", url, got, want)
				}
			}
		})
	}
}

func TestGitHubAPITokenPrefersGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh")
	t.Setenv("GITHUB_TOKEN", "github")
	if got := githubAPIToken(); got != "gh" {
		t.Fatalf("token = %q, want gh", got)
	}
	t.Setenv("GH_TOKEN", "")
	if got := githubAPIToken(); got != "github" {
		t.Fatalf("token = %q, want github", got)
	}
}
