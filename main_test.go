package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.org")
	if aus, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, aus)
	}
}

func TestGitLesen(t *testing.T) {
	d := t.TempDir()
	bare := filepath.Join(d, "x.git")
	gitTest(t, d, "init", "--bare", "-b", "main", bare)
	arbeit := filepath.Join(d, "arbeit")
	gitTest(t, d, "clone", bare, arbeit)
	os.MkdirAll(filepath.Join(arbeit, "ordner"), 0o755)
	os.WriteFile(filepath.Join(arbeit, "README.md"), []byte("# Hallo\n"), 0o644)
	os.WriteFile(filepath.Join(arbeit, "ordner", "a b.txt"), []byte("eins\nzwei\n"), 0o644)
	gitTest(t, arbeit, "add", ".")
	gitTest(t, arbeit, "commit", "-m", "Anfang")
	os.WriteFile(filepath.Join(arbeit, "ordner", "a b.txt"), []byte("eins\ndrei\n"), 0o644)
	gitTest(t, arbeit, "commit", "-am", "Zweiter")
	gitTest(t, arbeit, "push", "origin", "main")

	ctx := context.Background()
	if z := standardzweig(ctx, bare); z != "main" {
		t.Fatalf("Zweig %q", z)
	}
	l, err := liste(ctx, bare, "main", "")
	if err != nil || len(l) != 2 || l[0].Name != "ordner" || l[0].Art != "tree" {
		t.Fatalf("Liste: %v %+v", err, l)
	}
	l, _ = liste(ctx, bare, "main", "ordner")
	if len(l) != 1 || l[0].Pfad != "ordner/a b.txt" {
		t.Fatalf("Unterordner: %+v", l)
	}
	b, err := inhalt(ctx, bare, "main", "ordner/a b.txt")
	if err != nil || string(b) != "eins\ndrei\n" {
		t.Fatalf("Inhalt: %v %q", err, b)
	}
	c, err := protokoll(ctx, bare, "main", 10)
	if err != nil || len(c) != 2 || c[0].Betreff != "Zweiter" {
		t.Fatalf("Protokoll: %v %+v", err, c)
	}
	k, d2, err := commitMitDiff(ctx, bare, c[0].SHA)
	if err != nil || k.Betreff != "Zweiter" || len(d2) != 1 || d2[0].Plus != 1 || d2[0].Minus != 1 || d2[0].Name != "ordner/a b.txt" {
		t.Fatalf("Diff: %v %+v %+v", err, k, d2)
	}
	if _, err := liste(ctx, bare, "--output=/tmp/x", ""); err == nil {
		t.Fatal("Option als Ref angenommen")
	}
	if sichererRef("--output=x") || sichererRef("a..b") || sichererPfad("../etc") || !sichererRef("feature/x") {
		t.Fatal("Prüfung von Ref und Pfad")
	}
}

func TestSeitenRendern(t *testing.T) {
	v := ladeVorlagen()
	jetzt := time.Now()
	r := Repo{ID: "r1", Besitzer: "jonas", BesitzerID: "k1", Name: "foodr", Beschreibung: "Essen", Privat: true, Erstellt: jetzt, Geaendert: jetzt}
	a := Ansicht{R: r, Recht: "schreiben", Ref: "main", Zweige: []string{"main"}, Pfad: "a/b", Teile: teile("a/b"), Eltern: "a",
		Eintraege: []Eintrag{{Name: "c", Art: "tree", Pfad: "a/b/c"}, {Name: "d.go", Art: "blob", Pfad: "a/b/d.go", Groesse: 2048}},
		Readme:    "<h1>Hi</h1>", Klonen: "https://x/jonas/foodr.git", Letzter: &Commit{SHA: "abcdef1234", Kurz: "abcdef12", Betreff: "x", Zeit: jetzt}, Offen: 2}
	ich := Konto{ID: "k1", Name: "jonas"}
	c := Commit{SHA: "abcdef1234567", Kurz: "abcdef12", Autor: "J", Email: "j@x", Betreff: "Fix", Text: "mehr", Zeit: jetzt}
	faelle := map[string]any{
		"start":         map[string]any{"Repos": []Repo{r}, "Ich": ich},
		"neu":           map[string]any{"Ich": ich},
		"tokens":        map[string]any{"Tokens": []Token{{ID: "t", Name: "Laptop", Anfang: "fgr_1234", Erstellt: jetzt}}, "Neu": "fgr_x", "Ich": ich, "Basis": "https://x"},
		"baum":          a,
		"datei":         DateiAnsicht{Ansicht: a, Name: "d.go", Text: "package x\n", Zeilen: 2, Groesse: 10},
		"commits":       map[string]any{"A": a, "Commits": []Commit{c}},
		"commit":        map[string]any{"A": a, "C": c, "Dateien": []DiffDatei{{Name: "d.go", Plus: 1, Minus: 1, Zeilen: []DiffZeile{{Klasse: "abschnitt", Text: "@@"}, {Klasse: "plus", Text: "+a"}}}}, "Plus": 1, "Minus": 1},
		"einstellungen": map[string]any{"A": a, "Mitglieder": []Mitglied{{"anna", "Anna", "lesen"}}},
		"issues":        map[string]any{"A": a, "Issues": []Issue{{Nummer: 1, Titel: "Bug", Offen: true, Erstellt: jetzt}}, "OffeneListe": true},
		"issue":         map[string]any{"A": a, "I": Issue{Nummer: 1, Titel: "Bug", Offen: true}, "Kommentare": []Kommentar{{Verfasser: "j", HTML: mdSicher("**x**"), Erstellt: jetzt}}},
	}
	for name, d := range faelle {
		rec := httptest.NewRecorder()
		v.zeigen(rec, httptest.NewRequest("GET", "/", nil), name, Daten{App: "Forgr", D: d})
		if b := rec.Body.String(); !strings.Contains(b, "</html>") {
			t.Errorf("%s bricht ab: %s", name, b[max(0, len(b)-400):])
		}
	}
}

func TestNamen(t *testing.T) {
	for _, n := range []string{"neu", "x.git", "../a", "-x", "a b"} {
		if gueltigerName(n) {
			t.Errorf("%q angenommen", n)
		}
	}
	if !gueltigerName("foodr") || !gueltigerName("mc-serververwaltung") {
		t.Error("gültiger Name abgelehnt")
	}
}

// Das ganze Routing muss sich aufbauen lassen, ohne dass sich Muster beißen.
func TestWege(t *testing.T) {
	a := &App{sso: NeuesSSO("Test", filepath.Join(t.TempDir(), "sso.json"), "TEST_SSO_AUS"), v: ladeVorlagen()}
	a.sso.Frei = []string{"/statisch/", "/healthz"}
	h := a.wege()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/statisch/stil.css", nil))
	if rec.Code != 200 {
		t.Fatalf("statisch: %d", rec.Code)
	}
}
