package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type MR struct {
	ID, Titel, Text, Quelle, Ziel, Zustand, Verfasser, VerfasserID string
	QuelleSHA, ZielSHA, MergeSHA                                   string
	Nummer                                                         int
	Erstellt                                                       time.Time
}

func (m MR) Offen() bool { return m.Zustand == "offen" }

func (m MR) ZustandName() string {
	switch m.Zustand {
	case "zusammengefuehrt":
		return "zusammengeführt"
	case "geschlossen":
		return "geschlossen"
	}
	return "offen"
}

type MRBeitrag struct {
	Verfasser, Art, Datei, SHA string
	Zeile                      int
	HTML                       template.HTML
	Erstellt                   time.Time
	Veraltet                   bool
}

// ReviewZeile ist eine Diffzeile mit dem, was an ihr steht.
type ReviewZeile struct {
	DiffZeile
	Beitraege []MRBeitrag
}

type ReviewDatei struct {
	DiffDatei
	Z []ReviewZeile
}

const mrSpalten = `m.id::text, m.nummer, m.titel, m.text, m.quelle, m.ziel, m.zustand,
	COALESCE(k.name, ''), COALESCE(k.id::text, ''), m.quelle_sha, m.ziel_sha, m.merge_sha, m.erstellt
	FROM mrs m LEFT JOIN konten k ON k.id = m.verfasser`

func scanMR(z interface{ Scan(...any) error }) (MR, error) {
	var m MR
	err := z.Scan(&m.ID, &m.Nummer, &m.Titel, &m.Text, &m.Quelle, &m.Ziel, &m.Zustand,
		&m.Verfasser, &m.VerfasserID, &m.QuelleSHA, &m.ZielSHA, &m.MergeSHA, &m.Erstellt)
	return m, err
}

func (a *App) mrLaden(r *http.Request, repo Repo) (MR, error) {
	return scanMR(a.db.QueryRow(r.Context(), "SELECT "+mrSpalten+" WHERE m.repo=$1::uuid AND m.nummer::text=$2", repo.ID, r.PathValue("n")))
}

// zweigStand ist der Commit, auf dem ein Zweig steht.
func zweigStand(ctx context.Context, repo, zweig string) (string, error) {
	if !sichererRef(zweig) {
		return "", errors.New("Zweigname ungültig")
	}
	aus, err := gitAus(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+zweig+"^{commit}")
	return strings.TrimSpace(string(aus)), err
}

func (a *App) mrListe(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	offen := r.URL.Query().Get("zu") != "ja"
	rows, err := a.db.Query(r.Context(), "SELECT "+mrSpalten+" WHERE m.repo=$1::uuid AND (m.zustand='offen') = $2 ORDER BY m.nummer DESC", repo.ID, offen)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var l []MR
	for rows.Next() {
		m, err := scanMR(rows)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		l = append(l, m)
	}
	v := a.ansicht(r, repo, recht, "", "")
	a.v.zeigen(w, r, "mrs", a.daten(r, "Merge-Requests · "+repo.Voll(), "", map[string]any{
		"A": v, "MRs": l, "OffeneListe": offen, "Standard": standardzweig(r.Context(), a.pfad(repo.Besitzer, repo.Name)),
	}))
}

func (a *App) mrAnlegen(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, ich, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	if recht != "schreiben" {
		http.Error(w, "Merge-Requests brauchen Schreibrecht", http.StatusForbidden)
		return
	}
	hier := "/" + repo.Voll() + "/mr"
	titel, quelle, ziel := strings.TrimSpace(r.FormValue("titel")), r.FormValue("quelle"), r.FormValue("ziel")
	if titel == "" {
		titel = quelle
	}
	p := a.pfad(repo.Besitzer, repo.Name)
	qs, err1 := zweigStand(r.Context(), p, quelle)
	zs, err2 := zweigStand(r.Context(), p, ziel)
	if err1 != nil || err2 != nil || quelle == ziel || qs == zs {
		zurueck(w, r, hier, "Quelle und Ziel müssen zwei Zweige mit verschiedenem Stand sein")
		return
	}
	var nr int
	if err := a.db.QueryRow(r.Context(), `INSERT INTO mrs (repo, nummer, titel, text, quelle, ziel, verfasser)
		VALUES ($1::uuid, (SELECT COALESCE(max(nummer), 0) + 1 FROM mrs WHERE repo = $1::uuid), $2, $3, $4, $5, $6::uuid)
		RETURNING nummer`, repo.ID, titel, strings.TrimSpace(r.FormValue("text")), quelle, ziel, ich.ID).Scan(&nr); err != nil {
		zurueck(w, r, hier, "Nicht angelegt, bitte noch einmal")
		return
	}
	zurueck(w, r, hier+"/"+strconv.Itoa(nr), "Merge-Request angelegt")
}

func (a *App) mrZeigen(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	m, err := a.mrLaden(r, repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, p := r.Context(), a.pfad(repo.Besitzer, repo.Name)
	qs, zs := m.QuelleSHA, m.ZielSHA
	if qs == "" {
		qs, err = zweigStand(ctx, p, m.Quelle)
		if err == nil {
			zs, err = zweigStand(ctx, p, m.Ziel)
		}
	}
	fehlt := err != nil
	var dateien []DiffDatei
	var diffFehler string
	konflikt := false
	if !fehlt {
		if dateien, err = diffZwischen(ctx, p, zs, qs); err != nil {
			diffFehler = "Die Änderungen lassen sich nicht zeigen (zu groß oder ohne gemeinsamen Anfang)."
		}
		if m.Offen() {
			_, err := mergeBaum(ctx, p, zs, qs)
			konflikt = err != nil
		}
	}

	rows, err := a.db.Query(ctx, `SELECT COALESCE(k.name, ''), b.art, b.text, b.datei, b.zeile, b.sha, b.erstellt
		FROM mr_beitraege b LEFT JOIN konten k ON k.id = b.verfasser WHERE b.mr=$1::uuid ORDER BY b.id`, m.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type stelle struct {
		datei string
		zeile int
	}
	anZeile := map[stelle][]MRBeitrag{}
	urteil := map[string]string{}
	var allgemein []MRBeitrag
	for rows.Next() {
		var b MRBeitrag
		var text string
		if err := rows.Scan(&b.Verfasser, &b.Art, &text, &b.Datei, &b.Zeile, &b.SHA, &b.Erstellt); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		b.HTML, b.Veraltet = mdSicher(text), b.SHA != qs
		if b.Art != "kommentar" && !b.Veraltet {
			urteil[b.Verfasser] = b.Art // das letzte je Person zählt
		}
		if b.Datei != "" && !b.Veraltet {
			s := stelle{b.Datei, b.Zeile}
			anZeile[s] = append(anZeile[s], b)
		} else {
			allgemein = append(allgemein, b)
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var review []ReviewDatei
	plus, minus := 0, 0
	for _, d := range dateien {
		x := ReviewDatei{DiffDatei: d}
		for _, z := range d.Zeilen {
			rz := ReviewZeile{DiffZeile: z}
			if z.Neu > 0 {
				rz.Beitraege = anZeile[stelle{d.Name, z.Neu}]
			}
			x.Z = append(x.Z, rz)
		}
		plus, minus = plus+d.Plus, minus+d.Minus
		review = append(review, x)
	}
	ja, nein := 0, 0
	for _, u := range urteil {
		if u == "zustimmung" {
			ja++
		} else {
			nein++
		}
	}
	a.v.zeigen(w, r, "mr", a.daten(r, m.Titel+" · "+repo.Voll(), "", map[string]any{
		"A": a.ansicht(r, repo, recht, "", ""), "M": m, "Text": mdSicher(m.Text), "SHA": qs,
		"Dateien": review, "Plus": plus, "Minus": minus, "Beitraege": allgemein, "DiffFehler": diffFehler,
		"Fehlt": fehlt, "Konflikt": konflikt, "Ja": ja, "Nein": nein,
	}))
}

func (a *App) mrBeitrag(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, ich, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	m, err := a.mrLaden(r, repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hier := "/" + repo.Voll() + "/mr/" + strconv.Itoa(m.Nummer)
	if z := r.FormValue("zustand"); z != "" {
		if recht != "schreiben" && ich.ID != m.VerfasserID {
			http.Error(w, "Schließen darf, wer schreibt oder ihn angelegt hat", http.StatusForbidden)
			return
		}
		neu, vorher := "geschlossen", "offen"
		if z == "offen" {
			neu, vorher = "offen", "geschlossen"
		}
		if _, err := a.db.Exec(r.Context(), `UPDATE mrs SET zustand=$2, geaendert=now() WHERE id::text=$1 AND zustand=$3`, m.ID, neu, vorher); err != nil {
			zurueck(w, r, hier, "Nicht gespeichert")
			return
		}
	}
	art, text := r.FormValue("art"), strings.TrimSpace(r.FormValue("text"))
	switch art {
	case "zustimmung", "aenderung":
		if recht != "schreiben" || ich.ID == m.VerfasserID {
			http.Error(w, "Ein Urteil gibt, wer schreiben darf und nicht selbst der Verfasser ist", http.StatusForbidden)
			return
		}
	default:
		if art = "kommentar"; text == "" {
			zurueck(w, r, hier, "")
			return
		}
	}
	sha := r.FormValue("sha")
	if jetzt, err := zweigStand(r.Context(), a.pfad(repo.Besitzer, repo.Name), m.Quelle); m.Offen() && (err != nil || jetzt != sha) {
		zurueck(w, r, hier, "Der Zweig hat sich geändert, bitte noch einmal ansehen")
		return
	}
	datei, zeile := r.FormValue("datei"), 0
	if datei != "" {
		n, err := strconv.ParseInt(r.FormValue("zeile"), 10, 32)
		if err != nil || n < 1 || !sichererPfad(datei) {
			zurueck(w, r, hier, "Diese Zeile gibt es nicht")
			return
		}
		zeile = int(n)
	}
	if _, err := a.db.Exec(r.Context(), `INSERT INTO mr_beitraege (mr, verfasser, art, text, datei, zeile, sha)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)`, m.ID, ich.ID, art, text, datei, zeile, sha); err != nil {
		zurueck(w, r, hier, "Nicht gespeichert")
		return
	}
	zurueck(w, r, hier, "Gespeichert")
}

// mergeBaum rechnet den Merge ohne Arbeitsverzeichnis. Ein Fehler heißt
// Konflikt oder kein gemeinsamer Anfang.
func mergeBaum(ctx context.Context, repo, ziel, quelle string) (string, error) {
	aus, err := gitAus(ctx, repo, "merge-tree", "--write-tree", "--no-messages", ziel, quelle)
	if err != nil {
		return "", err
	}
	baum, _, _ := strings.Cut(strings.TrimSpace(string(aus)), "\n")
	return baum, nil
}

// gitName: Namen und Adressen stehen in Commit-Kopfzeilen.
func gitName(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == '<' || r == '>' || r == 127 {
			return -1
		}
		return r
	}, s))
}

func (a *App) mrZusammenfuehren(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, ich, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	if recht != "schreiben" {
		http.Error(w, "Zusammenführen braucht Schreibrecht", http.StatusForbidden)
		return
	}
	m, err := a.mrLaden(r, repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hier := "/" + repo.Voll() + "/mr/" + strconv.Itoa(m.Nummer)
	if !m.Offen() {
		zurueck(w, r, hier, "Der Merge-Request ist nicht offen")
		return
	}
	// Was begonnen ist, läuft zu Ende, auch wenn der Browser geht.
	ctx, ende := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	defer ende()
	p := a.pfad(repo.Besitzer, repo.Name)
	qs, err1 := zweigStand(ctx, p, m.Quelle)
	zs, err2 := zweigStand(ctx, p, m.Ziel)
	if err1 != nil || err2 != nil || qs != r.FormValue("sha") {
		zurueck(w, r, hier, "Der Zweig hat sich geändert, bitte noch einmal ansehen")
		return
	}
	baum, err := mergeBaum(ctx, p, zs, qs)
	if err != nil {
		zurueck(w, r, hier, "Das geht nicht ohne Konflikte, bitte lokal auflösen")
		return
	}
	name := gitName(ich.Anzeige)
	if name == "" {
		name = ich.Name
	}
	mail := gitName(ich.Email)
	if mail == "" {
		mail = ich.Name + "@forgr.invalid"
	}
	cmd := exec.CommandContext(ctx, "git", "-C", p, "commit-tree", baum, "-p", zs, "-p", qs,
		"-m", fmt.Sprintf("Merge %s in %s", m.Quelle, m.Ziel), "-m", fmt.Sprintf("%s (!%d)", gitName(m.Titel), m.Nummer))
	cmd.Env = slices.Concat(gitUmgebung(), []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + mail,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + mail,
	})
	aus, err := cmd.Output()
	if err != nil {
		log.Printf("merge %s !%d: %v", repo.Voll(), m.Nummer, err)
		zurueck(w, r, hier, "git konnte den Commit nicht schreiben")
		return
	}
	neu := strings.TrimSpace(string(aus))
	// Nur, wenn das Ziel noch dort steht, wo gerechnet wurde.
	if _, err := gitAus(ctx, p, "update-ref", "refs/heads/"+m.Ziel, neu, zs); err != nil {
		zurueck(w, r, hier, "Das Ziel hat sich geändert, bitte noch einmal")
		return
	}
	if _, err := a.db.Exec(ctx, `UPDATE mrs SET zustand='zusammengefuehrt', quelle_sha=$2, ziel_sha=$3, merge_sha=$4, geaendert=now() WHERE id::text=$1`,
		m.ID, qs, zs, neu); err != nil {
		log.Printf("merge %s !%d vermerken: %v", repo.Voll(), m.Nummer, err)
		zurueck(w, r, hier, "Zusammengeführt, aber nicht vermerkt")
		return
	}
	if _, err := a.db.Exec(ctx, `UPDATE repos SET geaendert=now() WHERE id::text=$1`, repo.ID); err != nil {
		log.Printf("merge %s: %v", repo.Voll(), err)
	}
	zurueck(w, r, hier, "Zusammengeführt")
}
