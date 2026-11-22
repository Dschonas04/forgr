package main

import (
	"bytes"
	"context"
	"html/template"
	"log"
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
		switch recht := a.hoechstesRecht(ctx, r, konto); recht {
		case "verwalten", "schreiben":
			return "schreiben"
		case "lesen":
			return "lesen"
		}
	}
	if !r.Privat {
		return "lesen"
	}
	return ""
}

// hoechstesRecht aus eigenem Eintrag und den Gruppen des Besitzers.
func (a *App) hoechstesRecht(ctx context.Context, r Repo, konto string) string {
	if konto == "" {
		return ""
	}
	var recht string
	if err := a.db.QueryRow(ctx, `SELECT x.recht FROM (
			SELECT recht FROM rechte WHERE repo=$1::uuid AND konto=$2::uuid
			UNION ALL
			SELECT tr.recht FROM team_rechte tr JOIN teams t ON t.id = tr.team JOIN team_mitglieder m ON m.team = t.id
			WHERE tr.repo=$1::uuid AND m.konto=$2::uuid AND t.besitzer=$3::uuid
		) x ORDER BY CASE x.recht WHEN 'verwalten' THEN 0 WHEN 'schreiben' THEN 1 ELSE 2 END LIMIT 1`,
		r.ID, konto, r.BesitzerID).Scan(&recht); err != nil {
		return ""
	}
	return recht
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
			OR EXISTS (SELECT 1 FROM team_rechte tr JOIN teams t ON t.id = tr.team JOIN team_mitglieder m ON m.team = t.id
				WHERE tr.repo = r.id AND m.konto = $1::uuid AND t.besitzer = r.besitzer)
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
	OffeneMR  int
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
	if err := a.db.QueryRow(r.Context(), `SELECT count(*) FROM mrs WHERE repo=$1::uuid AND zustand='offen'`, repo.ID).Scan(&v.OffeneMR); err != nil {
		log.Printf("Merge-Requests zählen: %v", err)
	}
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
	a.v.zeigen(w, r, "commits", a.daten(r, "Commits · "+repo.Voll(), "", map[string]any{"A": a.ansicht(r, repo, recht, ref, ""), "Commits": l, "Status": a.pruefungen(r.Context(), repo.ID, hashes(l)...)}))
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
		"C": c, "Pruefungen": a.pruefungen(r.Context(), repo.ID, c.SHA)[c.SHA], "Dateien": d, "Plus": plus, "Minus": minus}))
}

func hashes(l []Commit) []string {
	s := []string{}
	for _, c := range l {
		s = append(s, c.SHA)
	}
	return s
}

// ---------- Einstellungen ----------

type Mitglied struct{ Name, Anzeige, Recht string }

func (a *App) einstellungen(w http.ResponseWriter, r *http.Request) {
	repo, ich, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	if !a.darfVerwalten(r.Context(), repo, ich.ID) {
		http.Error(w, "Die Einstellungen ändert, wer das Repository verwaltet", http.StatusForbidden)
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
	teams, err1 := a.teamsVon(r.Context(), repo.BesitzerID, repo.ID)
	hooks, err2 := a.webhooksVon(r.Context(), repo.ID)
	if err1 != nil || err2 != nil {
		http.Error(w, "Datenbank", http.StatusInternalServerError)
		return
	}
	a.v.zeigen(w, r, "einstellungen", a.daten(r, "Einstellungen · "+repo.Voll(), "", map[string]any{
		"A": a.ansicht(r, repo, recht, "", ""), "Mitglieder": l,
		"Besitzer": ich.ID == repo.BesitzerID, "Teams": teams, "Hooks": hooks, "HooksAn": a.hooks.an()}))
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

func (a *App) verwalterFormular(w http.ResponseWriter, r *http.Request) (Repo, Konto, bool) {
	if !pruefeFormular(w, r) {
		return Repo{}, Konto{}, false
	}
	repo, ich, _, ok := a.zugang(w, r)
	if !ok {
		return repo, ich, false
	}
	if !a.darfVerwalten(r.Context(), repo, ich.ID) {
		http.Error(w, "Nur, wer das Repository verwaltet", http.StatusForbidden)
		return repo, ich, false
	}
	return repo, ich, true
}

func (a *App) einstellungenSpeichern(w http.ResponseWriter, r *http.Request) {
	repo, ich, ok := a.verwalterFormular(w, r)
	if !ok {
		return
	}
	privat := repo.Privat
	if ich.ID == repo.BesitzerID { // wer sieht es: das entscheidet der Besitzer
		privat = r.FormValue("oeffentlich") != "ja"
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE repos SET beschreibung=$2, privat=$3 WHERE id::text=$1`,
		repo.ID, strings.TrimSpace(r.FormValue("beschreibung")), privat); err != nil {
		zurueck(w, r, "/"+repo.Voll()+"/einstellungen", "Nicht gespeichert")
		return
	}
	zurueck(w, r, "/"+repo.Voll()+"/einstellungen", "Gespeichert")
}

func (a *App) mitgliedSetzen(w http.ResponseWriter, r *http.Request) {
	repo, ich, ok := a.verwalterFormular(w, r)
	if !ok {
		return
	}
	ziel := "/" + repo.Voll() + "/einstellungen"
	k, err := a.kontoNamens(r.Context(), strings.TrimSpace(r.FormValue("name")))
	if err != nil {
		zurueck(w, r, ziel, "Dieses Konto gibt es nicht. Es entsteht bei der ersten Anmeldung.")
		return
	}
	besitzer, recht := ich.ID == repo.BesitzerID, r.FormValue("recht")
	// Verwalten gibt und nimmt nur der Besitzer, die Bedingung steht im SQL.
	if recht == "verwalten" && !besitzer {
		http.Error(w, "Verwalten vergibt nur der Besitzer", http.StatusForbidden)
		return
	}
	q, args, meldung := `DELETE FROM rechte WHERE repo=$1::uuid AND konto=$2::uuid AND ($3 OR recht <> 'verwalten')`,
		[]any{repo.ID, k.ID, besitzer}, k.Name+" entfernt"
	switch recht {
	case "lesen", "schreiben", "verwalten":
		q, args = `INSERT INTO rechte (repo, konto, recht) VALUES ($1::uuid, $2::uuid, $4)
			ON CONFLICT (repo, konto) DO UPDATE SET recht=$4 WHERE $3 OR rechte.recht <> 'verwalten'`, append(args, recht)
		meldung = k.Name + " darf jetzt " + recht
	}
	if e, err := a.db.Exec(r.Context(), q, args...); err != nil || e.RowsAffected() == 0 {
		meldung = "Nicht geändert"
	}
	zurueck(w, r, ziel, meldung)
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
