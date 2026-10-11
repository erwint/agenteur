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

func fakeReleases(t *testing.T, tag string, binary []byte) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.WriteHeader(&tar.Header{Name: "coai", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(binary)
	tw.Close()
	gz.Close()
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
	tag, err := latestRelease(context.Background())
	if err != nil || tag != "v0.4.0" {
		t.Fatalf("%q %v", tag, err)
	}
	exe := filepath.Join(t.TempDir(), "coai")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyRelease(context.Background(), tag, exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(got) != "new binary" || st.Mode().Perm() != 0o755 {
		t.Fatalf("%q %v", got, st.Mode())
	}
	if err := applyRelease(context.Background(), "v9.9.9", exe); err == nil {
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
