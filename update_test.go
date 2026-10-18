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
	archive := bytes.NewBuffer(tarball("coai", binary))
	asset := fmt.Sprintf("/download/%s/coai_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
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
	exe := filepath.Join(t.TempDir(), "coai")
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

// After the repository is renamed to agenteur, GitHub redirects the old
// releases/latest to the new repository's, whose releases only carry
// agenteur archives. coai must follow both and install agenteur in its place.
func TestUpdateFollowsRenameToSuccessor(t *testing.T) {
	archive := tarball(successor, []byte("agenteur binary"))
	asset := fmt.Sprintf("/erwint/agenteur/releases/download/v0.5.0/agenteur_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/erwint/coai/releases/latest":
			http.Redirect(w, r, "/erwint/agenteur/releases/latest", http.StatusMovedPermanently)
		case "/erwint/agenteur/releases/latest":
			http.Redirect(w, r, "/erwint/agenteur/releases/tag/v0.5.0", http.StatusFound)
		case asset:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releaseBase
	releaseBase = srv.URL + "/erwint/coai/releases"
	t.Cleanup(func() { releaseBase = old })

	base, tag, err := latestRelease(context.Background())
	if err != nil || tag != "v0.5.0" || base != srv.URL+"/erwint/agenteur/releases" {
		t.Fatalf("%q %q %v", base, tag, err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "coai")
	if err := os.WriteFile(exe, []byte("coai binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyRelease(context.Background(), base, tag, exe); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "agenteur binary" {
		t.Fatalf("coai not replaced by agenteur: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, successor)); string(got) != "agenteur binary" {
		t.Fatalf("no agenteur link beside coai: %q", got)
	}
}

func TestSettingPrefersSuccessorName(t *testing.T) {
	t.Setenv("COAI_STATE", "old")
	if got := setting("STATE"); got != "old" {
		t.Fatal(got)
	}
	t.Setenv("AGENTEUR_STATE", "new")
	if got := setting("STATE"); got != "new" {
		t.Fatal(got)
	}
}
