package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Der ganze Weg mit echtem git: anlegen, pushen, ansehen.
func TestAblaufMitGit(t *testing.T) {
	adresse := os.Getenv("FORGR_TEST_DB")
	if adresse == "" {
		t.Skip("FORGR_TEST_DB ist nicht gesetzt")
	}
	c, err := pgxpool.ParseConfig(adresse)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.ConnConfig.Database, "pruef") {
		t.Fatal("FORGR_TEST_DB muss auf eine Datenbank pruef* zeigen")
	}
	ctx := context.Background()
	db := datenbank(ctx, adresse)
	defer db.Close()
	if _, err := db.Exec(ctx, `DELETE FROM konten`); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, v: ladeVorlagen(), repos: t.TempDir(),
		sso: NeuesSSO("Forgr", filepath.Join(t.TempDir(), "sso.json"), "FORGR_TEST_SSO_AUS")}
	a.sso.Durchlass = func(r *http.Request) bool { return gitPfad.MatchString(r.URL.Path) }
	k := ssoKonto{Name: "anna", Hash: ssoPasswortHash("nur-fuer-den-test")}
	a.sso.konten = append(a.sso.konten, k)
	rec := httptest.NewRecorder()
	a.sso.lokalAnmelden(rec, httptest.NewRequest("POST", "/", nil), k)
	kekse := append(rec.Result().Cookies(), &http.Cookie{Name: "formular", Value: strings.Repeat("a", 32)})

	s := httptest.NewServer(a.wege())
	defer s.Close()
	klient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ruf := func(methode, pfad string, f url.Values) (int, string) {
		if f == nil {
			f = url.Values{}
		}
		f.Set("token", strings.Repeat("a", 32))
		var inhalt io.Reader
		if methode != "GET" {
			inhalt = strings.NewReader(f.Encode())
		}
		r, err := http.NewRequest(methode, s.URL+pfad, inhalt)
		if err != nil {
			t.Fatal(err)
		}
		if methode != "GET" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", s.URL)
		}
		for _, c := range kekse {
			r.AddCookie(c)
		}
		res, err := klient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(b)
	}
	if code, _ := ruf("POST", "/neu", url.Values{"name": {"probe"}}); code != http.StatusSeeOther {
		t.Fatalf("anlegen: %d", code)
	}
	token := func(name string) string {
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO konten (kennung, name) VALUES ('konto:' || $1, $1)
			ON CONFLICT (kennung) DO UPDATE SET name = $1 RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		wert := "nur-fuer-den-test-" + name
		if _, err := db.Exec(ctx, `INSERT INTO tokens (konto, name, hash, anfang) VALUES ($1::uuid, 'test', $2, 'nur')`, id, tokenHash(wert)); err != nil {
			t.Fatal(err)
		}
		return wert
	}

	d := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "credential.helper=", "-c", "user.name=Test", "-c", "user.email=t@example.org"}, args...)...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if aus, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, aus)
		}
	}
	schreib := func(inhalt string) {
		if err := os.WriteFile(filepath.Join(d, "a.sql"), []byte(inhalt), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(s.URL + "/anna/probe.git")
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("anna", token("anna"))
	git("init", "-b", "main")
	schreib("-- alter Kommentar\nSELECT 1;\n")
	git("add", ".")
	git("commit", "-m", "Anfang")
	git("push", u.String(), "main")
	git("checkout", "-b", "feature/x")
	schreib("++ neue Zeile\nSELECT 1;\n")
	git("commit", "-am", "Kommentar")
	git("push", u.String(), "feature/x")

	for _, p := range []string{"/anna/probe/baum/feature/x/", "/anna/probe/datei/feature/x/a.sql", "/anna/probe/commits/feature/x"} {
		if code, _ := ruf("GET", p, nil); code != http.StatusOK {
			t.Errorf("Zweig mit Schrägstrich, %s: %d", p, code)
		}
	}

	bare := a.pfad("anna", "probe")
	l, err := protokoll(ctx, bare, "feature/x", 1)
	if err != nil || len(l) != 1 {
		t.Fatalf("Protokoll: %v", err)
	}
	if _, dd, err := commitMitDiff(ctx, bare, l[0].SHA); err != nil || len(dd) != 1 || dd[0].Plus != 1 || dd[0].Minus != 1 {
		t.Errorf("Diff verschluckt Zeilen: %v %+v", err, dd)
	}

	leser := token("leser")
	if _, err := db.Exec(ctx, `INSERT INTO rechte (repo, konto, recht)
		SELECT r.id, k.id, 'lesen' FROM repos r, konten k WHERE r.name = 'probe' AND k.name = 'leser'`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"service=git-receive-pack", "service=git-upload-pack&service=git-receive-pack"} {
		r, err := http.NewRequest("GET", s.URL+"/anna/probe.git/info/refs?"+q, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.SetBasicAuth("leser", leser)
		res, err := klient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode < 400 {
			t.Errorf("Leser bekommt receive-pack mit %s: %d", q, res.StatusCode)
		}
	}

	if _, b := ruf("GET", "/neu", nil); strings.Contains(b, "anonymes Klonen") {
		t.Error("Oberfläche verspricht anonymes Klonen")
	}
}
