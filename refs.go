package main

import (
	"context"
	"os"
	"strings"
)

// refUndPfad trennt "feature/x/ordner/datei" in Zweig oder Tag und Pfad.
// Schrägstriche dürfen in beiden stehen, es gewinnt der längste echte Name.
func refUndPfad(ctx context.Context, repo, rest string) (ref, pfad string) {
	rest = strings.Trim(rest, "/")
	if aus, err := gitAus(ctx, repo, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags"); err == nil {
		for _, z := range strings.Split(string(aus), "\n") {
			n, zweig := strings.CutPrefix(strings.TrimSpace(z), "refs/heads/")
			if !zweig {
				var tag bool
				if n, tag = strings.CutPrefix(n, "refs/tags/"); !tag {
					continue
				}
			}
			if len(n) > len(ref) && (rest == n || strings.HasPrefix(rest, n+"/")) {
				ref = n
			}
		}
	}
	if ref != "" {
		return ref, strings.Trim(rest[len(ref):], "/")
	}
	// Sonst ist das erste Stück gemeint, etwa ein Commit.
	ref, pfad, _ = strings.Cut(rest, "/")
	return ref, pfad
}

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
