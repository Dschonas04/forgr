package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
)

var (
	vollerHash = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	kurzerHash = regexp.MustCompile(`^[0-9a-f]{7,63}$`)
)

// refUndPfad trennt "feature/x/ordner/datei" in Stand und Pfad. Ein voller
// Hash ist immer der Commit, dann kommen Zweige, dann erst Tags.
func refUndPfad(ctx context.Context, repo, rest string) (ref, pfad string) {
	rest = strings.Trim(rest, "/")
	ref, pfad, _ = strings.Cut(rest, "/")
	if vollerHash.MatchString(ref) {
		return ref, pfad
	}
	for _, art := range []string{"refs/heads/", "refs/tags/"} {
		aus, err := gitAus(ctx, repo, "for-each-ref", "--format=%(refname)", art)
		if err != nil {
			break
		}
		name := ""
		for _, z := range strings.Split(string(aus), "\n") {
			n, ok := strings.CutPrefix(z, art)
			if ok && len(n) > len(name) && (rest == n || strings.HasPrefix(rest, n+"/")) {
				name = n
			}
		}
		if name != "" {
			return name, strings.Trim(rest[len(name):], "/")
		}
	}
	return ref, pfad
}

// aufloesen macht aus einem Namen den Commit. git selbst nähme bei gleichem
// Namen den Tag vor dem Zweig, darum bekommt es hier nie den kurzen Namen.
func aufloesen(ctx context.Context, repo, ref string) (string, error) {
	if !sichererRef(ref) {
		return "", errUnbekannterStand
	}
	if ref == "HEAD" {
		ref = standardzweig(ctx, repo)
	}
	kandidaten := []string{"refs/heads/" + ref, "refs/tags/" + ref}
	if vollerHash.MatchString(ref) {
		kandidaten = []string{ref}
	}
	for _, k := range kandidaten {
		if aus, err := gitAus(ctx, repo, "rev-parse", "--verify", "--quiet", k+"^{commit}"); err == nil {
			return strings.TrimSpace(string(aus)), nil
		}
	}
	if kurzerHash.MatchString(ref) {
		// --disambiguate kennt nur Objekte, keine Namen.
		aus, err := gitAus(ctx, repo, "rev-parse", "--disambiguate="+ref)
		if l := strings.Fields(string(aus)); err == nil && len(l) == 1 {
			if aus, err := gitAus(ctx, repo, "rev-parse", "--verify", "--quiet", l[0]+"^{commit}"); err == nil {
				return strings.TrimSpace(string(aus)), nil
			}
		}
	}
	return "", errUnbekannterStand
}

var errUnbekannterStand = errors.New("diesen Stand gibt es nicht")

// gitUmgebung: git bekommt nur, was es braucht, nicht die Umgebung der App
// mit dem Datenbankkennwort.
func gitUmgebung() []string {
	l := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C.UTF-8"}
	for _, n := range []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", "TZ"} {
		if v := os.Getenv(n); v != "" {
			l = append(l, n+"="+v)
		}
	}
	return l
}
