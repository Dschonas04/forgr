package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpiegelEingabe(t *testing.T) {
	l := spiegelListe("a/b push https://github.com/x/y.git, a/b push ssh://host/x, a/b pull https://n:p@host/x, ../a pull https://host/x")
	if len(l) != 1 {
		t.Fatalf("%+v", l)
	}
	if e := strings.Join(spiegelUmgebung(l[0], "anders.example", "testtoken"), "\n"); strings.Contains(e, "Authorization") {
		t.Error("Token geht an einen anderen Host")
	}
}

func TestSpiegelUeberschreibtNicht(t *testing.T) {
	d := t.TempDir()
	stand := func(name string) string {
		w, b := filepath.Join(d, name+"-w"), filepath.Join(d, name+".git")
		gitTest(t, d, "init", "-b", "main", w)
		gitTest(t, w, "commit", "--allow-empty", "-m", name)
		gitTest(t, d, "clone", "--bare", w, b)
		return b
	}
	hier, dort := stand("hier"), stand("dort")
	hs, err := zweigStand(t.Context(), hier, "main")
	if err != nil {
		t.Fatal(err)
	}
	ds, err := zweigStand(t.Context(), dort, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, richtung := range []string{"push", "pull"} {
		c := exec.Command("git", append([]string{"-C", hier}, Spiegel{Adresse: dort, Richtung: richtung}.befehl()...)...)
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if c.Run() == nil {
			t.Errorf("%s über fremde Historie ging durch", richtung)
		}
	}
	if x, err := zweigStand(t.Context(), hier, "main"); err != nil || x != hs {
		t.Error("pull hat überschrieben")
	}
	if x, err := zweigStand(t.Context(), dort, "main"); err != nil || x != ds {
		t.Error("push hat überschrieben")
	}
}
