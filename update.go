package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// version is set from the release tag at build time (-X main.version=...).
var version = "dev"

const repo = "erwint/coai"

// releaseBase is a variable so tests can serve their own release.
var releaseBase = "https://github.com/" + repo + "/releases"

// The default transport reads HTTPS_PROXY. Every call is bounded: an update
// check must never hold up the MCP server.
var (
	httpClient = &http.Client{Timeout: 30 * time.Second}
	noRedirect = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
)

// latestRelease reads the newest tag from the redirect on releases/latest,
// which needs no API quota.
func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseBase+"/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "coai/"+version)
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("%s/latest: %s, redirect %q", releaseBase, resp.Status, loc)
	}
	return tag, nil
}

// newer reports whether latest is a higher version than current. Only a
// release build is ever replaced: "dev" and git-describe versions such as
// "0.2.3-2-gabc1234-dirty" come from local builds, which must keep running
// local code.
func newer(current, latest string) bool {
	if strings.ContainsAny(current, "-+") {
		return false
	}
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// parseVersion reads major.minor.patch, ignoring a leading "v" and any
// pre-release suffix.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		end := strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' })
		if end >= 0 {
			p = p[:end]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// applyRelease downloads tag and renames it over exe. Writing beside the
// target keeps the rename on one filesystem, and a coai that is already
// running keeps the file it opened.
func applyRelease(ctx context.Context, tag, exe string) error {
	url := fmt.Sprintf("%s/download/%s/coai_%s_%s.tar.gz", releaseBase, tag, runtime.GOOS, runtime.GOARCH)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no coai binary in %s", url)
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) != "coai" || h.Typeflag != tar.TypeReg {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(exe), ".coai-update-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := io.Copy(tmp, tr); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmp.Name(), 0o755); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), exe)
	}
}

// updateIfStale installs a newer release at most once a day. The plugins
// install coai once and never again, so without this a machine keeps the first
// version it downloaded. It fails silently: it runs beside the MCP server, and
// a read-only install or an offline machine must not disturb the session.
// COAI_NO_UPDATE=1 turns it off.
func updateIfStale(ctx context.Context, stamp string) {
	if os.Getenv("COAI_NO_UPDATE") != "" {
		return
	}
	if st, err := os.Stat(stamp); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		return
	}
	// Stamp before checking so concurrently starting processes skip.
	if err := os.MkdirAll(filepath.Dir(stamp), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	tag, err := latestRelease(ctx)
	if err != nil || !newer(version, tag) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := applyRelease(ctx, tag, exe); err == nil {
		fmt.Fprintf(os.Stderr, "coai: updated %s to %s; new sessions use it\n", exe, tag)
	}
}
