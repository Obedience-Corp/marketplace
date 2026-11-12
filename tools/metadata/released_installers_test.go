//go:build released_installers

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The guard runs every released installer that predates Festival App support against
// this repository's catalog with the App entry present. A catalog such a binary cannot
// parse breaks every operation on that source, so this must pass before a merge.

type releasedInstaller struct {
	Version  string
	SHA256   map[string]string // GOARCH -> sha256 of festival-linux-<GOARCH>
	Outdated bool              // the `outdated` command exists (v0.3.10 and later)
}

var releasedInstallers = []releasedInstaller{
	{"v0.3.6", map[string]string{
		"amd64": "29aa3f4ca181318f3cd7d5acddf948dd270591f29ebee577e8cfc3ed96e25437",
		"arm64": "8581064564defe78f76683fede9741f46d52043d83b8b12822cc653f71d66114"}, false},
	{"v0.3.8", map[string]string{
		"amd64": "ae68d564dd6b5ed7877161598ffdb413d4a1f71e4cc6f16976b69b81bd379199",
		"arm64": "c662189c37186e7051bfab84d06d8533afc254323b94a3ec91aea374b2af9e12"}, false},
	{"v0.3.10", map[string]string{
		"amd64": "ba7aeea9c1c3f9883c7a018ca75b0c925eb909d148a67ab54938f115424690a3",
		"arm64": "459bfa068a5a1055a3d17ac3be52cd1bd454bf16ca9b200c91265bc5daecf701"}, true},
	{"v0.3.12", map[string]string{
		"amd64": "40bfa992859062eae6b04ea627b4e9bdfaf0ad816c3da8109ce139330336c031",
		"arm64": "38b2da94be76abeb8db8cb5993f4aff8ed829dd8ee6c34ccb70c128d7639e7e2"}, true},
}

const guardAppID = "obedience-corp/festival-app"

type candidateRepo struct {
	Bare, Baseline, Candidate string
	Packages                  int
}

func TestReleasedInstallersAcceptCandidateCatalog(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Fatalf("the guard drives linux installer binaries; run it in the container (GOOS=%s)", runtime.GOOS)
	}
	repo := candidateCatalog(t, nil)
	for _, inst := range releasedInstallers {
		t.Run(inst.Version, func(t *testing.T) {
			bin := downloadInstaller(t, inst)
			if problems := checkInstaller(t, bin, inst, repo); len(problems) > 0 {
				t.Fatalf("installer %s rejects the candidate catalog:\n  %s", inst.Version, strings.Join(problems, "\n  "))
			}
		})
	}
}

func TestReleasedInstallerGuardRejectsUnknownCatalogField(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Fatalf("run in the container (GOOS=%s)", runtime.GOOS)
	}
	repo := candidateCatalog(t, func(entry map[string]any) { entry["extensions"] = map[string]any{} })
	inst := releasedInstallers[len(releasedInstallers)-1]
	problems := checkInstaller(t, downloadInstaller(t, inst), inst, repo)
	if len(problems) == 0 {
		t.Fatal("an unknown catalog field passed the guard; the guard cannot fail")
	}
	t.Logf("negative control problems for %s:\n  %s", inst.Version, strings.Join(problems, "\n  "))
}

func guardRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func guardOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func copyCatalogFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func catalogHasApp(t *testing.T, document map[string]any) bool {
	t.Helper()
	packages, _ := document["packages"].([]any)
	for _, item := range packages {
		if entry, ok := item.(map[string]any); ok && entry["id"] == guardAppID {
			return true
		}
	}
	return false
}

func dropApp(t *testing.T, path string) {
	t.Helper()
	document, err := readJSONDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []any
	for _, item := range document["packages"].([]any) {
		if entry, ok := item.(map[string]any); !ok || entry["id"] != guardAppID {
			kept = append(kept, item)
		}
	}
	document["packages"] = kept
	raw, _ := json.Marshal(document)
	if raw, err = canonicalize(raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func candidateCatalog(t *testing.T, mutate func(entry map[string]any)) candidateRepo {
	t.Helper()
	work := t.TempDir() // container lane only: this test mutates the filesystem
	root := filepath.Join(work, "repo")
	for _, name := range []string{"obey-marketplace.json", "index.json"} {
		copyCatalogFile(t, filepath.Join("..", "..", name), filepath.Join(root, name))
	}
	manifests, err := filepath.Glob(filepath.Join("..", "..", "packages", "*", "*", "obey-package.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range manifests {
		relative, _ := filepath.Rel(filepath.Join("..", ".."), source)
		copyCatalogFile(t, source, filepath.Join(root, relative))
	}
	catalog, err := readJSONDocument(filepath.Join(root, "obey-marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	prepared := catalogHasApp(t, catalog)
	guardInit(t, root)
	if prepared {
		// The PR already carries the entry: the baseline is the tree without it.
		dropAppFiles(t, root)
		guardCommit(t, root, "baseline")
		restoreApp(t, root, manifests)
	} else {
		guardCommit(t, root, "baseline")
		transport, release := festivalAppFixtureFor(t, "0.2.0")
		setFestivalReleaseFor(t, transport, release)
		if err := prepareFestivalApp(context.Background(), root, "0.2.0", &http.Client{Transport: transport}); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		path := filepath.Join(root, "obey-marketplace.json")
		document, err := readJSONDocument(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range document["packages"].([]any) {
			if entry := item.(map[string]any); entry["id"] == guardAppID {
				mutate(entry)
			}
		}
		raw, _ := json.Marshal(document)
		if raw, err = canonicalize(raw); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	guardCommit(t, root, "candidate")
	history := strings.Fields(guardOutput(t, root, "rev-list", "--reverse", "HEAD"))
	if len(history) != 2 {
		t.Fatalf("expected baseline and candidate commits, got %v", history)
	}
	bare := filepath.Join(work, "bare.git")
	guardRun(t, work, "git", "init", "--bare", "-q", "-b", "main", bare)
	guardRun(t, root, "git", "push", "-q", bare, history[0]+":refs/heads/main", history[1]+":refs/heads/candidate")
	final, err := readJSONDocument(filepath.Join(root, "obey-marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	return candidateRepo{Bare: bare, Baseline: history[0], Candidate: history[1], Packages: len(final["packages"].([]any))}
}

func guardInit(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return
	}
	guardRun(t, root, "git", "init", "-q", "-b", "main")
}

func guardCommit(t *testing.T, root, message string) {
	t.Helper()
	guardRun(t, root, "git", "add", "-A")
	guardRun(t, root, "git", "-c", "user.name=guard", "-c", "user.email=guard@example.invalid", "commit", "-q", "--allow-empty", "-m", message)
}

// dropAppFiles and restoreApp support the PR-carries-the-entry case.
func dropAppFiles(t *testing.T, root string) {
	t.Helper()
	dropApp(t, filepath.Join(root, "obey-marketplace.json"))
	dropApp(t, filepath.Join(root, "index.json"))
	if err := os.RemoveAll(filepath.Join(root, "packages", "obedience-corp", "festival-app")); err != nil {
		t.Fatal(err)
	}
}

func restoreApp(t *testing.T, root string, manifests []string) {
	t.Helper()
	for _, name := range []string{"obey-marketplace.json", "index.json"} {
		copyCatalogFile(t, filepath.Join("..", "..", name), filepath.Join(root, name))
	}
	for _, source := range manifests {
		relative, _ := filepath.Rel(filepath.Join("..", ".."), source)
		copyCatalogFile(t, source, filepath.Join(root, relative))
	}
}

func downloadInstaller(t *testing.T, inst releasedInstaller) string {
	t.Helper()
	want, ok := inst.SHA256[runtime.GOARCH]
	if !ok {
		t.Fatalf("no pinned hash for %s on %s", inst.Version, runtime.GOARCH)
	}
	url := fmt.Sprintf("https://github.com/Obedience-Corp/festival-installer/releases/download/%s/festival-linux-%s", inst.Version, runtime.GOARCH)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("download %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download %s: HTTP %d", url, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128<<20))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("installer %s sha256 %s, pinned %s", inst.Version, got, want)
	}
	path := filepath.Join(t.TempDir(), "festival-"+inst.Version)
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runInstaller(ctx context.Context, bin, home string, args ...string) (stdout, stderr string, code int) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "FESTIVAL_HOME="+filepath.Join(home, "installer"), "OBEY_INSTALLER_HOME=")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return out.String(), errOut.String(), code
}

type installerEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type guardPackage struct {
	ID    string `json:"id"`
	Class string `json:"class"`
}

func checkInstaller(t *testing.T, bin string, inst releasedInstaller, repo candidateRepo) []string {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	installerHome := filepath.Join(home, "installer")
	if err := os.MkdirAll(installerHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installerHome, "config.toml"), []byte("[marketplaces]\ndefault = \"candidate\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	guardRun(t, ".", "git", "--git-dir", repo.Bare, "update-ref", "refs/heads/main", repo.Baseline)

	run := func(args ...string) (installerEnvelope, string, int, bool) {
		stdout, stderr, code := runInstaller(ctx, bin, home, args...)
		t.Logf("%s $ festival %s -> exit %d\nstdout: %s\nstderr: %s", inst.Version, strings.Join(args, " "), code, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		var envelope installerEnvelope
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			return envelope, stdout, code, false
		}
		return envelope, stdout, code, true
	}
	views := func(label string, envelope installerEnvelope) (count int, changed bool) {
		var list []struct {
			Name     string `json:"name"`
			Packages int    `json:"packages"`
			Changed  bool   `json:"changed"`
			Error    string `json:"error"`
		}
		if err := json.Unmarshal(envelope.Data, &list); err != nil {
			fail("%s: data is not a list of views: %v", label, err)
			return 0, false
		}
		found := false
		for _, view := range list {
			if view.Name != "candidate" {
				continue
			}
			found = true
			if view.Error != "" {
				fail("%s: candidate view error: %s", label, view.Error)
			}
			count, changed = view.Packages, view.Changed
		}
		if !found {
			fail("%s: no candidate view", label)
		}
		return count, changed
	}
	expectOK := func(label string, args ...string) (installerEnvelope, bool) {
		envelope, stdout, code, parsed := run(args...)
		if !parsed || code != 0 || !envelope.OK {
			fail("%s: festival %s exit %d, stdout %q", label, strings.Join(args, " "), code, strings.TrimSpace(stdout))
			return envelope, false
		}
		return envelope, true
	}
	refuseApp := func(target string) {
		envelope, stdout, code, parsed := run("install", target, "--json")
		if !parsed || code == 0 || envelope.OK || envelope.Error == nil ||
			(envelope.Error.Code != "E_INSTALL_TARGET" && envelope.Error.Code != "E_PLUGIN_TARGET") {
			fail("install %s: want E_INSTALL_TARGET or E_PLUGIN_TARGET, got exit %d stdout %q", target, code, strings.TrimSpace(stdout))
		}
	}
	checkStatusAndList := func(label string) {
		expectOK(label+" status", "status", "--json")
		if envelope, ok := expectOK(label+" marketplace list", "marketplace", "list", "--json"); ok {
			if count, _ := views(label+" marketplace list", envelope); count != repo.Packages {
				fail("%s marketplace list: candidate has %d packages, want %d", label, count, repo.Packages)
			}
		}
	}

	// 1. Add at baseline.
	if _, stderr, code := runInstaller(ctx, bin, home, "marketplace", "add", repo.Bare, "--name", "candidate"); code != 0 {
		t.Logf("marketplace add stderr: %s", stderr)
		fail("marketplace add at baseline exited %d", code)
		return problems
	}
	// 2. Advance to candidate and refresh.
	guardRun(t, ".", "git", "--git-dir", repo.Bare, "update-ref", "refs/heads/main", repo.Candidate)
	if envelope, ok := expectOK("refresh", "marketplace", "refresh", "candidate", "--json"); ok {
		if _, changed := views("refresh", envelope); !changed {
			fail("refresh: candidate view reports changed=false")
		}
	}
	// 3. The candidate view lists every package.
	if envelope, ok := expectOK("marketplace list", "marketplace", "list", "--json"); ok {
		if count, _ := views("marketplace list", envelope); count != repo.Packages {
			fail("marketplace list: candidate has %d packages, want %d", count, repo.Packages)
		}
	}
	// 4. browse lists the App as a product.
	if envelope, ok := expectOK("browse", "browse", "--json"); ok {
		var data struct {
			Groups []struct {
				Packages []guardPackage `json:"packages"`
			} `json:"groups"`
			Packages []guardPackage `json:"packages"`
		}
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			fail("browse: decode data: %v", err)
		}
		all := data.Packages
		for _, group := range data.Groups {
			all = append(all, group.Packages...)
		}
		found := false
		for _, pkg := range all {
			if pkg.ID == guardAppID {
				found = true
				if pkg.Class != "product" {
					fail("browse: %s class %q, want product", guardAppID, pkg.Class)
				}
			}
		}
		if !found {
			fail("browse: %s not listed among %d packages", guardAppID, len(all))
		}
	}
	// 5. Read-only commands.
	expectOK("list", "list", "--json")
	expectOK("status", "status", "--json")
	if inst.Outdated {
		expectOK("outdated", "outdated", "--json")
	}
	// 6. Only an App install is refused, with the normal target error.
	for _, target := range []string{"app", "festival-app", guardAppID} {
		refuseApp(target)
	}
	// 7. The refused attempts changed nothing.
	checkStatusAndList("after refused installs")
	// 8. A fresh home adding the repository directly at candidate.
	guardRun(t, ".", "git", "--git-dir", repo.Bare, "update-ref", "refs/heads/main", repo.Candidate)
	fresh := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fresh, "installer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "installer", "config.toml"), []byte("[marketplaces]\ndefault = \"candidate\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runInstaller(ctx, bin, fresh, "marketplace", "add", repo.Bare, "--name", "candidate"); code != 0 {
		fail("fresh add at candidate exited %d", code)
	} else {
		stdout, _, code := runInstaller(ctx, bin, fresh, "marketplace", "list", "--json")
		var envelope installerEnvelope
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || code != 0 || !envelope.OK {
			fail("fresh marketplace list exit %d stdout %q", code, strings.TrimSpace(stdout))
		} else if count, _ := views("fresh marketplace list", envelope); count != repo.Packages {
			fail("fresh marketplace list: candidate has %d packages, want %d", count, repo.Packages)
		}
	}
	return problems
}
