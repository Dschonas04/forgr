package main

import (
	"bytes"
	"context"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

type Repo struct {
	ID, Besitzer, BesitzerID, Name, Beschreibung string
	Privat                                       bool
	Erstellt, Geaendert                          time.Time
}

func (r Repo) Voll() string { return r.Besitzer + "/" + r.Name }

const repoSpalten = `r.id::text, k.name, k.id::text, r.name, r.beschreibung, r.privat, r.erstellt, r.geaendert`

func (a *App) repoLaden(ctx context.Context, besitzer, name string) (Repo, error) {
	var r Repo
	err := a.db.QueryRow(ctx, "SELECT "+repoSpalten+` FROM repos r JOIN konten k ON k.id = r.besitzer
		WHERE k.name = $1 AND r.name = $2`, besitzer, name).
		Scan(&r.ID, &r.Besitzer, &r.BesitzerID, &r.Name, &r.Beschreibung, &r.Privat, &r.Erstellt, &r.Geaendert)
	return r, err
}

// rechtVon: "schreiben", "lesen" oder "" (kein Zugriff). Der Besitzer
// schreibt, Mitglieder nach ihrem Recht, öffentliche Repos liest jeder.
func (a *App) rechtVon(ctx context.Context, r Repo, konto string) string {
	if konto != "" && konto == r.BesitzerID {
		return "schreiben"
	}
	if konto != "" {
		var recht string
		if a.db.QueryRow(ctx, `SELECT recht FROM rechte WHERE repo=$1::uuid AND konto=$2::uuid`, r.ID, konto).Scan(&recht) == nil {
			return recht
		}
	}
	if !r.Privat {
		return "lesen"
	}
	return ""
}

// zugang lädt das Repo aus der Adresse und prüft, ob die angemeldete Person
// es sehen darf. Ein privates Repo ohne Recht gibt es für sie nicht.
func (a *App) zugang(w http.ResponseWriter, r *http.Request) (Repo, Konto, string, bool) {
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return Repo{}, ich, "", false
	}
	repo, err := a.repoLaden(r.Context(), r.PathValue("b"), r.PathValue("r"))
	if err != nil {
		http.NotFound(w, r)
		return repo, ich, "", false
	}
	recht := a.rechtVon(r.Context(), repo, ich.ID)
	if recht == "" {
		http.NotFound(w, r)
		return repo, ich, "", false
	}
	return repo, ich, recht, true
}

// ---------- Übersicht und Anlegen ----------

func (a *App) start(w http.ResponseWriter, r *http.Request) {
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows, err := a.db.Query(r.Context(), "SELECT "+repoSpalten+` FROM repos r JOIN konten k ON k.id = r.besitzer
		WHERE r.besitzer = $1::uuid OR NOT r.privat OR EXISTS (SELECT 1 FROM rechte x WHERE x.repo = r.id AND x.konto = $1::uuid)
		ORDER BY r.geaendert DESC`, ich.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var l []Repo
	for rows.Next() {
		var x Repo
		if rows.Scan(&x.ID, &x.Besitzer, &x.BesitzerID, &x.Name, &x.Beschreibung, &x.Privat, &x.Erstellt, &x.Geaendert) == nil {
			l = append(l, x)
		}
	}
	a.v.zeigen(w, r, "start", a.daten(r, "Repositories", "start", map[string]any{"Repos": l, "Ich": ich}))
}

func (a *App) neuFormular(w http.ResponseWriter, r *http.Request) {
	ich, _ := a.ich(r)
	a.v.zeigen(w, r, "neu", a.daten(r, "Neues Repository", "neu", map[string]any{"Ich": ich}))
}

func (a *App) neuAnlegen(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	quelle := strings.TrimSpace(r.FormValue("quelle"))
	if name == "" && quelle != "" {
		name = strings.TrimSuffix(path.Base(quelle), ".git")
	}
	if !gueltigerName(name) {
		zurueck(w, r, "/neu", "Name ungültig: Buchstaben, Ziffern, Punkt, Strich, Unterstrich")
		return
	}
	if quelle != "" {
		u, err := url.Parse(quelle)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			zurueck(w, r, "/neu", "Importieren geht nur von einer https-Adresse ohne Zugangsdaten")
			return
		}
	}
	ziel := a.pfad(ich.Name, name)
	if _, err := os.Stat(ziel); err == nil {
		zurueck(w, r, "/neu", "Das Repository gibt es schon")
		return
	}
	var id string
	if err := a.db.QueryRow(r.Context(), `INSERT INTO repos (besitzer, name, beschreibung, privat) VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
		ich.ID, name, strings.TrimSpace(r.FormValue("beschreibung")), r.FormValue("oeffentlich") != "ja").Scan(&id); err != nil {
		zurueck(w, r, "/neu", "Das Repository gibt es schon")
		return
	}
	os.MkdirAll(filepath.Dir(ziel), 0o750)
	var befehl *exec.Cmd
	if quelle != "" {
		ctx, abbruch := context.WithTimeout(r.Context(), 10*time.Minute)
		defer abbruch()
		befehl = exec.CommandContext(ctx, "git",
			// Nur https, keine Umleitung woandershin.
			"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false",
			"clone", "--bare", "--no-local", "--", quelle, ziel)
	} else {
		befehl = exec.Command("git", "init", "--bare", "-b", "main", "--", ziel)
	}
	befehl.Env = gitUmgebung()
	if aus, err := befehl.CombinedOutput(); err != nil {
		a.db.Exec(r.Context(), `DELETE FROM repos WHERE id::text=$1`, id)
		os.RemoveAll(ziel)
		zurueck(w, r, "/neu", "git: "+kurz(string(aus)))
		return
	}
	gitAus(r.Context(), ziel, "config", "http.receivepack", "true")
	zurueck(w, r, "/"+ich.Name+"/"+name, "Repository angelegt")
}

func kurz(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---------- Ansehen ----------

type Ansicht struct {
	R         Repo
	Recht     string
	Ref       string
	Zweige    []string
	Pfad      string
	Teile     []Teil
	Eltern    string
	Eintraege []Eintrag
	Readme    template.HTML
	Leer      bool
	Klonen    string
	Letzter   *Commit
	Offen     int
}

type Teil struct{ Name, Pfad string }

func teile(p string) []Teil {
	var l []Teil
	akt := ""
	for _, x := range strings.Split(p, "/") {
		if x == "" {
			continue
		}
		akt = strings.TrimPrefix(akt+"/"+x, "/")
		l = append(l, Teil{x, akt})
	}
	return l
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func (a *App) ansicht(r *http.Request, repo Repo, recht, ref, pfad string) Ansicht {
	p := a.pfad(repo.Besitzer, repo.Name)
	eltern := ""
	if i := strings.LastIndexByte(pfad, '/'); i > 0 {
		eltern = pfad[:i]
	}
	v := Ansicht{R: repo, Recht: recht, Ref: ref, Pfad: pfad, Teile: teile(pfad), Eltern: eltern,
		Zweige: zweige(r.Context(), p), Klonen: basis(r) + "/" + repo.Voll() + ".git"}
	v.Leer = len(v.Zweige) == 0
	a.db.QueryRow(r.Context(), `SELECT count(*) FROM issues WHERE repo=$1::uuid AND offen`, repo.ID).Scan(&v.Offen)
	return v
}

func (a *App) repoStart(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	ref := standardzweig(r.Context(), a.pfad(repo.Besitzer, repo.Name))
	a.baumZeigen(w, r, repo, recht, ref, "")
}

func (a *App) baum(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	ref, pfad := refUndPfad(r.Context(), a.pfad(repo.Besitzer, repo.Name), r.PathValue("rest"))
	if !sichererRef(ref) || !sichererPfad(pfad) {
		http.NotFound(w, r)
		return
	}
	a.baumZeigen(w, r, repo, recht, ref, pfad)
}

func (a *App) baumZeigen(w http.ResponseWriter, r *http.Request, repo Repo, recht, ref, pfad string) {
	v := a.ansicht(r, repo, recht, ref, pfad)
	p := a.pfad(repo.Besitzer, repo.Name)
	if !v.Leer {
		var err error
		v.Eintraege, err = liste(r.Context(), p, ref, pfad)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if c, err := protokoll(r.Context(), p, ref, 1); err == nil && len(c) > 0 {
			v.Letzter = &c[0]
		}
		for _, e := range v.Eintraege {
			if e.Art == "blob" && strings.HasPrefix(strings.ToLower(e.Name), "readme") && e.Groesse < 1<<20 {
				if b, err := inhalt(r.Context(), p, ref, e.Pfad); err == nil {
					var out bytes.Buffer
					if strings.HasSuffix(strings.ToLower(e.Name), ".md") && md.Convert(b, &out) == nil {
						v.Readme = template.HTML(out.String())
					} else {
						v.Readme = template.HTML("<pre>" + template.HTMLEscapeString(string(b)) + "</pre>")
					}
				}
				break
			}
		}
	}
	a.v.zeigen(w, r, "baum", a.daten(r, repo.Voll(), "", v))
}

type DateiAnsicht struct {
	Ansicht
	Name    string
	Text    string
	Zeilen  int
	Bild    bool
	Binaer  bool
	Gross   bool
	Groesse int64
}

var bildEndungen = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

func (a *App) datei(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	ref, pfad := refUndPfad(r.Context(), a.pfad(repo.Besitzer, repo.Name), r.PathValue("rest"))
	if !sichererRef(ref) || !sichererPfad(pfad) || pfad == "" {
		http.NotFound(w, r)
		return
	}
	b, err := inhalt(r.Context(), a.pfad(repo.Besitzer, repo.Name), ref, pfad)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := DateiAnsicht{Ansicht: a.ansicht(r, repo, recht, ref, pfad), Name: path.Base(pfad), Groesse: int64(len(b))}
	switch {
	case bildEndungen[strings.ToLower(path.Ext(pfad))]:
		v.Bild = true
	case !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0:
		v.Binaer = true
	case len(b) > 1<<20:
		v.Gross = true
	default:
		v.Text = string(b)
		v.Zeilen = strings.Count(v.Text, "\n") + 1
	}
	a.v.zeigen(w, r, "datei", a.daten(r, pfad+" · "+repo.Voll(), "", v))
}

// roh liefert eine Datei aus. Nie als HTML: eine Datei aus einem Repository
// wäre sonst eine Seite auf dem Ursprung von Forgr.
func (a *App) roh(w http.ResponseWriter, r *http.Request) {
	repo, _, _, ok := a.zugang(w, r)
	if !ok {
		return
	}
	ref, pfad := refUndPfad(r.Context(), a.pfad(repo.Besitzer, repo.Name), r.PathValue("rest"))
	if !sichererRef(ref) || !sichererPfad(pfad) {
		http.NotFound(w, r)
		return
	}
	b, err := inhalt(r.Context(), a.pfad(repo.Besitzer, repo.Name), ref, pfad)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	typ := "application/octet-stream"
	art := "attachment"
	if e := strings.ToLower(path.Ext(pfad)); bildEndungen[e] {
		typ, art = mime.TypeByExtension(e), "inline"
	} else if utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 {
		typ, art = "text/plain; charset=utf-8", "inline"
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(art, map[string]string{"filename": path.Base(pfad)}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	w.Write(b)
}

func (a *App) commits(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	ref, rest := refUndPfad(r.Context(), a.pfad(repo.Besitzer, repo.Name), r.PathValue("rest"))
	if rest != "" || !sichererRef(ref) {
		http.NotFound(w, r)
		return
	}
	l, _ := protokoll(r.Context(), a.pfad(repo.Besitzer, repo.Name), ref, 200)
	a.v.zeigen(w, r, "commits", a.daten(r, "Commits · "+repo.Voll(), "", map[string]any{"A": a.ansicht(r, repo, recht, ref, ""), "Commits": l}))
}

func (a *App) commit(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	sha := r.PathValue("sha")
	if !sichererRef(sha) {
		http.NotFound(w, r)
		return
	}
	c, d, err := commitMitDiff(r.Context(), a.pfad(repo.Besitzer, repo.Name), sha)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	plus, minus := 0, 0
	for _, x := range d {
		plus, minus = plus+x.Plus, minus+x.Minus
	}
	a.v.zeigen(w, r, "commit", a.daten(r, c.Betreff+" · "+repo.Voll(), "", map[string]any{
		"A": a.ansicht(r, repo, recht, standardzweig(r.Context(), a.pfad(repo.Besitzer, repo.Name)), ""),
		"C": c, "Dateien": d, "Plus": plus, "Minus": minus}))
}

// ---------- Einstellungen ----------

type Mitglied struct{ Name, Anzeige, Recht string }

func (a *App) einstellungen(w http.ResponseWriter, r *http.Request) {
	repo, ich, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	if ich.ID != repo.BesitzerID {
		http.Error(w, "Nur der Besitzer ändert die Einstellungen", http.StatusForbidden)
		return
	}
	rows, _ := a.db.Query(r.Context(), `SELECT k.name, k.anzeige, x.recht FROM rechte x JOIN konten k ON k.id = x.konto WHERE x.repo=$1::uuid ORDER BY k.name`, repo.ID)
	var l []Mitglied
	for rows != nil && rows.Next() {
		var m Mitglied
		if rows.Scan(&m.Name, &m.Anzeige, &m.Recht) == nil {
			l = append(l, m)
		}
	}
	if rows != nil {
		rows.Close()
	}
	a.v.zeigen(w, r, "einstellungen", a.daten(r, "Einstellungen · "+repo.Voll(), "", map[string]any{
		"A": a.ansicht(r, repo, recht, "", ""), "Mitglieder": l}))
}

func (a *App) besitzerFormular(w http.ResponseWriter, r *http.Request) (Repo, bool) {
	if !pruefeFormular(w, r) {
		return Repo{}, false
	}
	repo, ich, _, ok := a.zugang(w, r)
	if !ok {
		return repo, false
	}
	if ich.ID != repo.BesitzerID {
		http.Error(w, "Nur der Besitzer", http.StatusForbidden)
		return repo, false
	}
	return repo, true
}

func (a *App) einstellungenSpeichern(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.besitzerFormular(w, r)
	if !ok {
		return
	}
	a.db.Exec(r.Context(), `UPDATE repos SET beschreibung=$2, privat=$3 WHERE id::text=$1`,
		repo.ID, strings.TrimSpace(r.FormValue("beschreibung")), r.FormValue("oeffentlich") != "ja")
	zurueck(w, r, "/"+repo.Voll()+"/einstellungen", "Gespeichert")
}

func (a *App) mitgliedSetzen(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.besitzerFormular(w, r)
	if !ok {
		return
	}
	ziel := "/" + repo.Voll() + "/einstellungen"
	k, err := a.kontoNamens(r.Context(), strings.TrimSpace(r.FormValue("name")))
	if err != nil {
		zurueck(w, r, ziel, "Dieses Konto gibt es nicht. Es entsteht bei der ersten Anmeldung.")
		return
	}
	switch recht := r.FormValue("recht"); recht {
	case "lesen", "schreiben":
		a.db.Exec(r.Context(), `INSERT INTO rechte (repo, konto, recht) VALUES ($1::uuid, $2::uuid, $3)
			ON CONFLICT (repo, konto) DO UPDATE SET recht=$3`, repo.ID, k.ID, recht)
		zurueck(w, r, ziel, k.Name+" darf jetzt "+recht)
	default:
		a.db.Exec(r.Context(), `DELETE FROM rechte WHERE repo=$1::uuid AND konto=$2::uuid`, repo.ID, k.ID)
		zurueck(w, r, ziel, k.Name+" entfernt")
	}
}

func (a *App) repoLoeschen(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.besitzerFormular(w, r)
	if !ok {
		return
	}
	if r.FormValue("bestaetigung") != repo.Name {
		zurueck(w, r, "/"+repo.Voll()+"/einstellungen", "Zum Löschen den Namen des Repositorys eintippen")
		return
	}
	a.db.Exec(r.Context(), `DELETE FROM repos WHERE id::text=$1`, repo.ID)
	os.RemoveAll(a.pfad(repo.Besitzer, repo.Name))
	zurueck(w, r, "/", repo.Voll()+" gelöscht")
}
