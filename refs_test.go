package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Kein Name darf vorgeben, ein anderer Stand zu sein.
func TestRefsNichtUntergeschoben(t *testing.T) {
	d := t.TempDir()
	bare, arbeit := filepath.Join(d, "x.git"), filepath.Join(d, "arbeit")
	gitTest(t, d, "init", "--bare", "-b", "main", bare)
	gitTest(t, d, "init", "-b", "main", arbeit)
	stand := func(text string) string {
		if err := os.MkdirAll(filepath.Join(arbeit, "ordner"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"a", "ordner/a"} {
			if err := os.WriteFile(filepath.Join(arbeit, p), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitTest(t, arbeit, "add", ".")
		gitTest(t, arbeit, "commit", "-m", text)
		aus, err := gitAus(t.Context(), arbeit, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(aus))
	}
	echt := stand("echt")
	gitTest(t, arbeit, "push", bare, "main")
	falsch := stand("falsch")
	gitTest(t, arbeit, "push", bare, falsch+":refs/tags/main")
	gitTest(t, arbeit, "push", bare, falsch+":refs/tags/v1")
	gitTest(t, arbeit, "push", bare, falsch+":refs/tags/"+echt+"/ordner")

	if z := zweige(t.Context(), bare); len(z) != 1 || z[0] != "main" {
		t.Errorf("Zweige: %v", z)
	}
	for rest, soll := range map[string]string{"main/a": "echt", echt + "/ordner/a": "echt", echt[:10] + "/a": "echt", "v1/a": "falsch"} {
		ref, pfad := refUndPfad(t.Context(), bare, rest)
		if b, err := inhalt(t.Context(), bare, ref, pfad); err != nil || string(b) != soll {
			t.Errorf("%s: %q statt %q (%v)", rest, b, soll, err)
		}
	}
	gitTest(t, bare, "update-ref", "-d", "refs/tags/main")
	gitTest(t, arbeit, "push", bare, falsch+":refs/tags/main/ordner")
	ref, pfad := refUndPfad(t.Context(), bare, "main/ordner/a")
	if b, err := inhalt(t.Context(), bare, ref, pfad); err != nil || string(b) != "echt" {
		t.Errorf("Tag fängt Zweig und Pfad: %q %v", b, err)
	}
	if l, err := protokoll(t.Context(), bare, "main", 1); err != nil || len(l) != 1 || l[0].SHA != echt {
		t.Errorf("Protokoll: %v", err)
	}
}
