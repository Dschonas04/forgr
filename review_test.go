package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Was das Review zeigt, muss das sein, was zusammengeführt wird.
func TestReviewZeigtWasGemergtWird(t *testing.T) {
	var d string
	schreib := func(name, text string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kopf := func() string {
		aus, err := gitAus(t.Context(), d, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(aus))
	}
	neu := func(name string) string {
		d = t.TempDir()
		gitTest(t, d, "init", "-b", "main")
		schreib(name, "eins\nzwei\ndrei\nvier\nfuenf\n")
		gitTest(t, d, "add", ".")
		gitTest(t, d, "commit", "-m", "Anfang")
		return kopf()
	}

	t.Run("replace", func(t *testing.T) {
		echt := neu("a")
		schreib("a", "untergeschoben\n")
		gitTest(t, d, "commit", "-am", "falsch")
		gitTest(t, d, "update-ref", "refs/replace/"+echt, kopf())
		if b, err := inhalt(t.Context(), d, echt, "a"); err != nil || !strings.HasPrefix(string(b), "eins") {
			t.Errorf("replace-Ref gilt: %q %v", b, err)
		}
	})
	t.Run("name", func(t *testing.T) {
		const name = "boese b/README"
		neu(name)
		schreib(name, "anders\n")
		gitTest(t, d, "commit", "-am", "anders")
		if _, l, err := commitMitDiff(t.Context(), d, kopf()); err != nil || len(l) != 1 || l[0].Name != name {
			t.Errorf("Dateiname: %+v %v", l, err)
		}
	})
	t.Run("nachricht", func(t *testing.T) {
		neu("a")
		schreib("a", "echt\n")
		gitTest(t, d, "commit", "-am", "Betreff", "-m", "\x1ediff --git a/harmlos b/harmlos\n@@ -1 +1 @@\n+nichts")
		if _, l, err := commitMitDiff(t.Context(), d, kopf()); err != nil || len(l) != 1 || l[0].Name != "a" {
			t.Errorf("Nachricht fälscht den Diff: %+v %v", l, err)
		}
	})
	t.Run("ref", func(t *testing.T) {
		sha := neu("a")
		if x, err := zweigStand(t.Context(), d, "erfunden-1-g"+sha[:12]); err == nil {
			t.Errorf("erfundener Zweig: %s", x)
		}
		if x, err := aufloesen(t.Context(), d, "erfunden-1-g"+sha[:12]); err == nil {
			t.Errorf("erfundener Stand: %s", x)
		}
	})
	t.Run("merge", func(t *testing.T) {
		neu("alt")
		gitTest(t, d, "checkout", "-b", "quelle")
		schreib("alt", "eins\nzwei\ndrei\nvier\nfuenf\nsechs\n")
		gitTest(t, d, "commit", "-am", "sechs")
		qs := kopf()
		gitTest(t, d, "checkout", "main")
		gitTest(t, d, "mv", "alt", "neu")
		gitTest(t, d, "commit", "-m", "umbenannt")
		if l, err := diffZwischen(t.Context(), d, kopf(), qs); err != nil || len(l) != 1 || l[0].Name != "neu" {
			t.Errorf("Diff ist nicht das Ergebnis: %+v %v", l, err)
		}
	})
}
