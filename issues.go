package main

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Issue struct {
	ID, Titel, Verfasser string
	Nummer, Kommentare   int
	Offen                bool
	Erstellt, Geaendert  time.Time
	Labels               []string
	Meilenstein          string
}

func (i Issue) LabelText() string { return strings.Join(i.Labels, ", ") }

type Kommentar struct {
	Verfasser string
	HTML      template.HTML
	Erstellt  time.Time
}

func mdSicher(s string) template.HTML {
	var b bytes.Buffer
	if md.Convert([]byte(s), &b) != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(b.String())
}

func (a *App) issues(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	offen := r.URL.Query().Get("zu") != "ja"
	rows, err := a.db.Query(r.Context(), `SELECT i.id::text, i.nummer, i.titel, COALESCE(k.name, ''), i.offen, i.erstellt, i.geaendert,
		(SELECT count(*) FROM kommentare c WHERE c.issue = i.id), i.labels, i.meilenstein
		FROM issues i LEFT JOIN konten k ON k.id = i.verfasser WHERE i.repo=$1::uuid AND i.offen=$2 AND ($3 = '' OR $3 = ANY(i.labels)) AND ($4 = '' OR i.meilenstein = $4)
		ORDER BY i.nummer DESC`, repo.ID, offen, r.URL.Query().Get("label"), r.URL.Query().Get("meilenstein"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var l []Issue
	for rows.Next() {
		var i Issue
		if rows.Scan(&i.ID, &i.Nummer, &i.Titel, &i.Verfasser, &i.Offen, &i.Erstellt, &i.Geaendert, &i.Kommentare, &i.Labels, &i.Meilenstein) == nil {
			l = append(l, i)
		}
	}
	rows.Close()
	ms, err := a.meilensteine(r.Context(), repo.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.v.zeigen(w, r, "issues", a.daten(r, "Issues · "+repo.Voll(), "", map[string]any{
		"A": a.ansicht(r, repo, recht, "", ""), "Issues": l, "OffeneListe": offen, "Meilensteine": ms}))
}

func (a *App) issueAnlegen(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, ich, _, ok := a.zugang(w, r)
	if !ok {
		return
	}
	titel := strings.TrimSpace(r.FormValue("titel"))
	if titel == "" {
		zurueck(w, r, "/"+repo.Voll()+"/issues", "Titel fehlt")
		return
	}
	var id string
	var nr int
	err := a.db.QueryRow(r.Context(), `INSERT INTO issues (repo, nummer, titel, verfasser)
		VALUES ($1::uuid, (SELECT COALESCE(max(nummer), 0) + 1 FROM issues WHERE repo = $1::uuid), $2, $3::uuid)
		RETURNING id::text, nummer`, repo.ID, titel, ich.ID).Scan(&id, &nr)
	if err != nil {
		zurueck(w, r, "/"+repo.Voll()+"/issues", "Fehler: "+err.Error())
		return
	}
	if t := strings.TrimSpace(r.FormValue("text")); t != "" {
		a.db.Exec(r.Context(), `INSERT INTO kommentare (issue, verfasser, text) VALUES ($1::uuid, $2::uuid, $3)`, id, ich.ID, t)
	}
	zurueck(w, r, "/"+repo.Voll()+"/issues/"+strconv.Itoa(nr), "Issue angelegt")
}

func (a *App) issueLaden(r *http.Request, repo Repo) (Issue, error) {
	var i Issue
	err := a.db.QueryRow(r.Context(), `SELECT i.id::text, i.nummer, i.titel, COALESCE(k.name, ''), i.offen, i.erstellt, i.geaendert, i.labels, i.meilenstein
		FROM issues i LEFT JOIN konten k ON k.id = i.verfasser WHERE i.repo=$1::uuid AND i.nummer::text=$2`, repo.ID, r.PathValue("n")).
		Scan(&i.ID, &i.Nummer, &i.Titel, &i.Verfasser, &i.Offen, &i.Erstellt, &i.Geaendert, &i.Labels, &i.Meilenstein)
	return i, err
}

func (a *App) issue(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	i, err := a.issueLaden(r, repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT COALESCE(k.name, ''), c.text, c.erstellt FROM kommentare c
		LEFT JOIN konten k ON k.id = c.verfasser WHERE c.issue=$1::uuid ORDER BY c.id`, i.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var l []Kommentar
	for rows.Next() {
		var k Kommentar
		var t string
		if rows.Scan(&k.Verfasser, &t, &k.Erstellt) == nil {
			k.HTML = mdSicher(t)
			l = append(l, k)
		}
	}
	a.v.zeigen(w, r, "issue", a.daten(r, i.Titel+" · "+repo.Voll(), "", map[string]any{
		"A": a.ansicht(r, repo, recht, "", ""), "I": i, "Kommentare": l}))
}

func (a *App) issueKommentar(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, ich, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	i, err := a.issueLaden(r, repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ziel := "/" + repo.Voll() + "/issues/" + strconv.Itoa(i.Nummer)
	if t := strings.TrimSpace(r.FormValue("text")); t != "" {
		a.db.Exec(r.Context(), `INSERT INTO kommentare (issue, verfasser, text) VALUES ($1::uuid, $2::uuid, $3)`, i.ID, ich.ID, t)
	}
	// Schließen und Öffnen: wer schreiben darf oder es angelegt hat.
	if z := r.FormValue("zustand"); z != "" && (recht == "schreiben" || ich.Name == i.Verfasser) {
		a.db.Exec(r.Context(), `UPDATE issues SET offen=$2 WHERE id::text=$1`, i.ID, z == "offen")
	}
	a.db.Exec(r.Context(), `UPDATE issues SET geaendert=now() WHERE id::text=$1`, i.ID)
	zurueck(w, r, ziel, "")
}
