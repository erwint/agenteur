package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		want            bool
	}{
		{"0.3.0", "v0.3.1", true},
		{"0.3.0", "v0.10.0", true},
		{"1.0.0", "v0.9.9", false},
		{"0.3.0", "v0.3.0", false},
		{"dev", "v9.0.0", false},
		{"0.2.3-2-gabc1234-dirty", "v0.3.0", false},
		{"b80391f", "v0.3.0", false},
		{"0.3.0", "not-a-tag", false},
	} {
		if got := newer(c.current, c.latest); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.current, c.latest, got)
		}
	}
}

// tarball builds a release archive holding a binary called name.
func tarball(name string, binary []byte) []byte {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(binary)
	tw.Close()
	gz.Close()
	return archive.Bytes()
}

func fakeReleases(t *testing.T, tag string, binary []byte) {
	t.Helper()
	archive := bytes.NewBuffer(tarball(binaryName, binary))
	asset := fmt.Sprintf("/download/%s/agenteur_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case asset:
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releaseBase
	releaseBase = srv.URL
	t.Cleanup(func() { releaseBase = old })
}

func TestApplyLatestRelease(t *testing.T) {
	fakeReleases(t, "v0.4.0", []byte("new binary"))
	base, tag, err := latestRelease(context.Background())
	if err != nil || tag != "v0.4.0" || base != releaseBase {
		t.Fatalf("%q %q %v", base, tag, err)
	}
	exe := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyRelease(context.Background(), base, tag, exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(got) != "new binary" || st.Mode().Perm() != 0o755 {
		t.Fatalf("%q %v", got, st.Mode())
	}
	if err := applyRelease(context.Background(), base, "v9.9.9", exe); err == nil {
		t.Fatal("missing asset reported as success")
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Fatal("failed update damaged the binary")
	}
}

func TestUpdateCheckIsThrottled(t *testing.T) {
	stamp := filepath.Join(t.TempDir(), "state", ".last-update-check")
	releaseBase = "http://127.0.0.1:1" // unreachable: a check must fail quietly
	t.Cleanup(func() { releaseBase = "https://github.com/" + repo + "/releases" })
	updateIfStale(context.Background(), stamp)
	st, err := os.Stat(stamp)
	if err != nil {
		t.Fatal("check did not record its time")
	}
	updateIfStale(context.Background(), stamp)
	if st2, _ := os.Stat(stamp); !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("checked again within a day")
	}
}

// coai's bridge release left agenteur at ~/.local/bin/coai, where old coai
// plugins run it. Updating must keep that path working and add an agenteur
// link for the agenteur plugins, following a repository redirect on the way.
func TestUpdateKeepsCoaiInstallAndLinksAgenteur(t *testing.T) {
	archive := tarball(binaryName, []byte("agenteur 2"))
	asset := fmt.Sprintf("/erwint/agenteur/releases/download/v2.0.0/agenteur_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/erwint/old/releases/latest":
			http.Redirect(w, r, "/erwint/agenteur/releases/latest", http.StatusMovedPermanently)
		case "/erwint/agenteur/releases/latest":
			http.Redirect(w, r, "/erwint/agenteur/releases/tag/v2.0.0", http.StatusFound)
		case asset:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releaseBase
	releaseBase = srv.URL + "/erwint/old/releases"
	t.Cleanup(func() { releaseBase = old })

	base, tag, err := latestRelease(context.Background())
	if err != nil || tag != "v2.0.0" || base != srv.URL+"/erwint/agenteur/releases" {
		t.Fatalf("%q %q %v", base, tag, err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "coai")
	if err := os.WriteFile(exe, []byte("agenteur 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyRelease(context.Background(), base, tag, exe); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{exe, filepath.Join(dir, binaryName)} {
		if got, _ := os.ReadFile(p); string(got) != "agenteur 2" {
			t.Fatalf("%s: %q", p, got)
		}
	}
}

func TestDefaultStateKeepsCoaiDatabase(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HOME", cfg)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	d, _ := os.UserConfigDir()
	if got := defaultState(); got != filepath.Join(d, "agenteur", "state.db") {
		t.Fatalf("fresh machine: %s", got)
	}
	legacy := filepath.Join(d, "coai", "state.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultState(); got != legacy {
		t.Fatalf("machine that ran coai: %s", got)
	}
}

func TestSettingFallsBackToCoaiName(t *testing.T) {
	t.Setenv("COAI_STATE", "old")
	if got := setting("STATE"); got != "old" {
		t.Fatal(got)
	}
	t.Setenv("AGENTEUR_STATE", "new")
	if got := setting("STATE"); got != "new" {
		t.Fatal(got)
	}
}

func TestSwitchNoticeNamesTheClientsCommands(t *testing.T) {
	codex, claude := switchNotice("codex"), switchNotice("claude")
	if !strings.Contains(codex, "codex plugin add agenteur@agenteur") || strings.Contains(codex, "claude plugin") {
		t.Fatal(codex)
	}
	if !strings.Contains(claude, "claude plugin install agenteur@agenteur") || strings.Contains(claude, "codex plugin") {
		t.Fatal(claude)
	}
	// Installing agenteur comes before removing coai.
	if strings.Index(claude, "install agenteur") > strings.Index(claude, "uninstall coai") {
		t.Fatal("coai removed before agenteur is installed")
	}
}
