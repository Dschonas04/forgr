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
	"time"

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

	// Merge-Request: anlegen, ansehen, kommentieren, zusammenführen.
	qs, err := zweigStand(ctx, bare, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := ruf("POST", "/anna/probe/mr", url.Values{"quelle": {"feature/x"}, "ziel": {"main"}, "titel": {"Kommentar neu"}}); code != http.StatusSeeOther {
		t.Fatalf("MR anlegen: %d", code)
	}
	if code, b := ruf("GET", "/anna/probe/mr/1", nil); code != 200 || !strings.Contains(b, "ohne Konflikte") || !strings.Contains(b, "neue Zeile") {
		t.Fatalf("MR ansehen: %d", code)
	}
	ruf("POST", "/anna/probe/mr/1", url.Values{"sha": {qs}, "datei": {"a.sql"}, "zeile": {"1"}, "text": {"ZEILENKOMMENTAR"}})
	if _, b := ruf("GET", "/anna/probe/mr/1", nil); !strings.Contains(b, `<div class="zkommentar">`) || !strings.Contains(b, "ZEILENKOMMENTAR") {
		t.Error("Zeilenkommentar fehlt")
	}
	if code, _ := ruf("POST", "/anna/probe/mr/1", url.Values{"sha": {qs}, "art": {"zustimmung"}}); code != http.StatusForbidden {
		t.Errorf("Verfasserin stimmt sich selbst zu: %d", code)
	}
	vorMerge, err := zweigStand(ctx, bare, "main")
	if err != nil {
		t.Fatal(err)
	}
	ruf("POST", "/anna/probe/mr/1/zusammen", url.Values{"sha": {vorMerge}, "ziel_sha": {vorMerge}})
	if jetzt, err := zweigStand(ctx, bare, "main"); err != nil || jetzt != vorMerge {
		t.Error("Merge mit veraltetem Stand")
	}
	ruf("POST", "/anna/probe/mr/1/zusammen", url.Values{"sha": {qs}, "ziel_sha": {qs}})
	if jetzt, err := zweigStand(ctx, bare, "main"); err != nil || jetzt != vorMerge {
		t.Error("Merge auf ein anderes Ziel als das angesehene")
	}
	ruf("POST", "/anna/probe/mr/1/zusammen", url.Values{"sha": {qs}, "ziel_sha": {vorMerge}})
	if b, err := inhalt(ctx, bare, "main", "a.sql"); err != nil || !strings.HasPrefix(string(b), "++ neue Zeile") {
		t.Errorf("Merge nicht angekommen: %v", err)
	}

	// Zwei Änderungen an derselben Zeile.
	git("checkout", "main")
	git("pull", u.String(), "main")
	git("checkout", "-b", "konflikt")
	schreib("links\nSELECT 1;\n")
	git("commit", "-am", "links")
	git("push", u.String(), "konflikt")
	git("checkout", "main")
	schreib("rechts\nSELECT 1;\n")
	git("commit", "-am", "rechts")
	git("push", u.String(), "main")
	ruf("POST", "/anna/probe/mr", url.Values{"quelle": {"konflikt"}, "ziel": {"main"}})
	if code, b := ruf("GET", "/anna/probe/mr/2", nil); code != 200 || !strings.Contains(b, "Konflikte</span>") {
		t.Errorf("Konflikt nicht erkannt: %d", code)
	}

	if code, _ := ruf("GET", "/anna/probe/blame/main/a.sql", nil); code != 200 {
		t.Errorf("Blame: %d", code)
	}
	ruf("POST", "/anna/probe/releases", url.Values{"tag": {"v1.0.0"}, "zweig": {"main"}, "text": {"NOTIZEN"}})
	if code, b := ruf("GET", "/anna/probe/releases", nil); code != 200 || !strings.Contains(b, "NOTIZEN") {
		t.Errorf("Release: %d", code)
	}
	if code, b := ruf("GET", "/anna/probe/archiv/v1.0.0", nil); code != 200 || !strings.HasPrefix(b, "\x1f\x8b") {
		t.Errorf("Archiv: %d", code)
	}
	ruf("POST", "/anna/probe/issues", url.Values{"titel": {"Einordnen"}})
	ruf("POST", "/anna/probe/issues/1/einordnen", url.Values{"labels": {"bug, eilig"}, "meilenstein": {"1.0"}})
	if code, b := ruf("GET", "/anna/probe/issues?label=eilig", nil); code != 200 || !strings.Contains(b, "Einordnen") || !strings.Contains(b, "0 von 1 fertig") {
		t.Errorf("Labels: %d", code)
	}

	for _, p := range []string{"", "/mr", "/mr?zu=ja", "/mr/1", "/mr/2", "/releases", "/blame/main/a.sql", "/issues", "/issues/1", "/commits/main", "/commits/HEAD", "/baum/main/", "/datei/main/a.sql"} {
		if code, b := ruf("GET", "/anna/probe"+p, nil); code != 200 || !strings.Contains(b, "</html>") {
			t.Errorf("Seite %q: %d", p, code)
		}
	}
	// Ein Urteil gibt es nur am offenen Request, refs/replace nimmt niemand an.
	if _, err := db.Exec(ctx, `UPDATE mrs SET verfasser=(SELECT id FROM konten WHERE name='leser') WHERE nummer=2`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []url.Values{
		{"art": {"zustimmung"}, "zustand": {"zu"}, "sha": {qs}},
		{"art": {"zustimmung"}, "sha": {qs}},
	} {
		ruf("POST", "/anna/probe/mr/2", f)
	}
	var urteile int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mr_beitraege WHERE art='zustimmung'`).Scan(&urteile); err != nil || urteile != 0 {
		t.Errorf("Urteil am geschlossenen Request: %d %v", urteile, err)
	}
	ersatz := exec.Command("git", "-c", "credential.helper=", "push", u.String(), "main:refs/replace/"+qs)
	ersatz.Dir, ersatz.Env = d, append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if ersatz.Run() == nil {
		t.Error("refs/replace angenommen")
	}

	// Webhook, Commit-Status, Gruppe.
	empfangen := make(chan string, 4)
	ziel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		empfangen <- r.Header.Get("X-Forgr-Ereignis") + " " + r.Header.Get("X-Forgr-Signatur")
	}))
	defer ziel.Close()
	a.hooks = neueHooks(ziel.URL, "testgeheimnis")
	ruf("POST", "/anna/probe/webhook", url.Values{"adresse": {"http://169.254.169.254/latest"}})
	ruf("POST", "/anna/probe/webhook", url.Values{"adresse": {ziel.URL + "/hook"}})
	var anzahl int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM webhooks`).Scan(&anzahl); err != nil || anzahl != 1 {
		t.Errorf("Webhook-Ziele: %d %v", anzahl, err)
	}
	git("commit", "--allow-empty", "-m", "fuer den Hook")
	git("push", u.String(), "main")
	select {
	case e := <-empfangen:
		if !strings.HasPrefix(e, "push sha256=") {
			t.Errorf("Webhook: %q", e)
		}
	case <-time.After(10 * time.Second):
		t.Error("kein Webhook nach dem Push")
	}

	stand, err := zweigStand(ctx, bare, "main")
	if err != nil {
		t.Fatal(err)
	}
	melde := func(name, tok string) int {
		r, err := http.NewRequest("POST", s.URL+"/api/repos/anna/probe/status/"+stand, strings.NewReader(`{"zustand":"success","name":"bau"}`))
		if err != nil {
			t.Fatal(err)
		}
		if name != "" {
			r.SetBasicAuth(name, tok)
		} else {
			for _, c := range kekse {
				r.AddCookie(c)
			}
		}
		res, err := klient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := melde("leser", leser); c != http.StatusForbidden {
		t.Errorf("Leser meldet Status: %d", c)
	}
	if c := melde("", ""); c != http.StatusUnauthorized {
		t.Errorf("Status mit Sitzung: %d", c)
	}
	if c := melde("anna", "nur-fuer-den-test-anna"); c != http.StatusOK {
		t.Errorf("Status: %d", c)
	}
	if _, b := ruf("GET", "/anna/probe/commit/"+stand, nil); !strings.Contains(b, "bau: erfolgreich") {
		t.Error("Status fehlt am Commit")
	}

	ruf("POST", "/gruppen", url.Values{"name": {"Team"}})
	var team string
	if err := db.QueryRow(ctx, `SELECT id::text FROM teams`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	ruf("POST", "/gruppen", url.Values{"id": {team}, "aktion": {"dazu"}, "name": {"leser"}})
	ruf("POST", "/anna/probe/team", url.Values{"team": {team}, "recht": {"schreiben"}})
	if c := melde("leser", leser); c != http.StatusOK {
		t.Errorf("Gruppe schreibt nicht: %d", c)
	}
	// Eine fremde Gruppe öffnet nichts, auch mit Eintrag in der Datenbank.
	fremd := token("fremd")
	if _, err := db.Exec(ctx, `WITH t AS (INSERT INTO teams (besitzer, name) SELECT id, 'Fremd' FROM konten WHERE name='fremd' RETURNING id, besitzer),
		m AS (INSERT INTO team_mitglieder (team, konto) SELECT id, besitzer FROM t)
		INSERT INTO team_rechte (repo, team, recht) SELECT r.id, t.id, 'verwalten' FROM repos r, t WHERE r.name='probe'`); err != nil {
		t.Fatal(err)
	}
	if c := melde("fremd", fremd); c != http.StatusNotFound {
		t.Errorf("fremde Gruppe öffnet das Repo: %d", c)
	}
	ruf("POST", "/anna/probe/team", url.Values{"team": {team}})
	if c := melde("leser", leser); c != http.StatusForbidden {
		t.Errorf("entzogene Gruppe schreibt weiter: %d", c)
	}
	for _, p := range []string{"/gruppen", "/anna/probe/einstellungen"} {
		if code, b := ruf("GET", p, nil); code != 200 || !strings.Contains(b, "</html>") {
			t.Errorf("%s: %d", p, code)
		}
	}

	if _, b := ruf("GET", "/neu", nil); strings.Contains(b, "anonymes Klonen") {
		t.Error("Oberfläche verspricht anonymes Klonen")
	}
}
