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

const repo = "erwint/agenteur"

// binaryName is the binary in each release archive, agenteur_<os>_<arch>.
const binaryName = "agenteur"

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
// which needs no API quota. It returns the releases URL the tag was found
// under, following a repository redirect if GitHub serves one first.
func latestRelease(ctx context.Context) (base, tag string, err error) {
	url := releaseBase + "/latest"
	for hops := 0; hops < 5; hops++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", "", err
		}
		req.Header.Set("User-Agent", "agenteur/"+version)
		resp, err := noRedirect.Do(req)
		if err != nil {
			return "", "", err
		}
		resp.Body.Close()
		loc, err := resp.Location()
		if err != nil {
			return "", "", fmt.Errorf("%s: %s, no redirect", url, resp.Status)
		}
		next := loc.String()
		if i := strings.LastIndex(next, "/tag/"); i >= 0 && strings.HasPrefix(next[i+5:], "v") {
			return next[:i], next[i+5:], nil
		}
		if !strings.HasSuffix(next, "/releases/latest") {
			return "", "", fmt.Errorf("%s: %s, redirect %q", url, resp.Status, next)
		}
		url = next
	}
	return "", "", fmt.Errorf("%s: too many redirects", releaseBase)
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

// applyRelease downloads tag from base and renames it over exe. Writing beside
// the target keeps the rename on one filesystem, and a process that is
// already running keeps the file it opened. An install that coai's bridge
// release turned into agenteur still lives at ~/.local/bin/coai, where old
// coai plugins start it; it is updated in place and gains an agenteur link
// beside it for the agenteur plugins.
func applyRelease(ctx context.Context, base, tag, exe string) error {
	err := applyArchive(ctx, base, tag, binaryName, exe)
	if err == errNoArchive {
		return fmt.Errorf("release %s has no archive for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}
	if err == nil && filepath.Base(exe) != binaryName {
		link := filepath.Join(filepath.Dir(exe), binaryName)
		if _, statErr := os.Lstat(link); os.IsNotExist(statErr) {
			_ = os.Symlink(filepath.Base(exe), link)
		}
	}
	return err
}

var errNoArchive = fmt.Errorf("no such archive")

func applyArchive(ctx context.Context, base, tag, name, exe string) error {
	url := fmt.Sprintf("%s/download/%s/%s_%s_%s.tar.gz", base, tag, name, runtime.GOOS, runtime.GOARCH)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNoArchive
	}
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
			return fmt.Errorf("no %s binary in %s", name, url)
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) != name || h.Typeflag != tar.TypeReg {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(exe), ".agenteur-update-*")
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
// install agenteur once and never again, so without this a machine keeps the first
// version it downloaded. It fails silently: it runs beside the MCP server, and
// a read-only install or an offline machine must not disturb the session.
// AGENTEUR_NO_UPDATE=1 (or coai's COAI_NO_UPDATE=1) turns it off.
func updateIfStale(ctx context.Context, stamp string) {
	if setting("NO_UPDATE") != "" {
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
	base, tag, err := latestRelease(ctx)
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
	if err := applyRelease(ctx, base, tag, exe); err == nil {
		fmt.Fprintf(os.Stderr, "agenteur: updated %s to %s; new sessions use it\n", exe, tag)
	}
}

// setting reads an environment setting as AGENTEUR_<name>, falling back to
// the name coai used (COAI_<name>), which keeps working.
func setting(name string) string {
	if v := os.Getenv("AGENTEUR_" + name); v != "" {
		return v
	}
	return os.Getenv("COAI_" + name)
}

// defaultState is the database used without AGENTEUR_STATE. A machine that
// ran coai keeps coai's database: old coai processes share it while their
// sessions last, so it is never moved.
func defaultState() string {
	d, _ := os.UserConfigDir()
	if legacy := filepath.Join(d, "coai", "state.json"); fileExists(legacy) {
		return legacy
	}
	return filepath.Join(d, "agenteur", "state.db")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// startedAsCoai reports whether an old coai plugin started this binary: the
// bridge release installed agenteur as ~/.local/bin/coai, which those plugins
// run by that name.
func startedAsCoai() bool {
	return filepath.Base(os.Args[0]) == "coai"
}

// switchNotice asks the agent of an old coai plugin to offer the switch to
// the agenteur plugin and, once the user agrees, run it. Neither client can
// move an installed plugin to another marketplace, so the agent does it. The
// agenteur plugin is installed before the coai one is removed, so a failure
// part way leaves a working plugin.
func switchNotice(kind string) string {
	const head = "coai is now agenteur. This session runs it through the old coai plugin, which keeps working but no longer gets updates. Tell the user once, and offer to switch them to the agenteur plugin by running these shell commands for them. Run them only after the user agrees, one at a time, stopping at the first that fails: "
	const tail = " Sessions and history carry over; the new skills are agenteur:join, agenteur:leave and agenteur:list."
	if kind == "codex" {
		return head + "codex plugin marketplace add erwint/agenteur; codex plugin add agenteur@agenteur; codex plugin remove coai@coai; codex plugin marketplace remove coai. Then the user restarts Codex." + tail
	}
	return head + "claude plugin marketplace add erwint/agenteur; claude plugin install agenteur@agenteur; claude plugin uninstall coai@coai; claude plugin marketplace remove coai. Then the user restarts Claude Code, rejoins with /agenteur:join, and can turn on updates under /plugin, Manage marketplaces, agenteur, Enable auto-update." + tail
}
