package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// CI meldet mit Benutzername und Token, wie git. Eine Sitzung gilt hier nicht.
var statusPfad = regexp.MustCompile(`^/api/repos/([a-z0-9][a-z0-9._-]*)/([A-Za-z0-9][A-Za-z0-9._-]*)/status/([0-9a-f]{40}|[0-9a-f]{64})$`)

type Pruefung struct {
	SHA, Name, Zustand, Adresse, Text string
	Zeit                              time.Time
}

func (p Pruefung) Klasse() string {
	switch p.Zustand {
	case "success":
		return "gut"
	case "failure", "error":
		return "schlecht"
	}
	return "warn"
}

func (p Pruefung) Wort() string {
	switch p.Zustand {
	case "success":
		return "erfolgreich"
	case "failure":
		return "fehlgeschlagen"
	case "error":
		return "Fehler"
	}
	return "läuft"
}

func (a *App) pruefungen(ctx context.Context, repo string, shas ...string) map[string][]Pruefung {
	m := map[string][]Pruefung{}
	rows, err := a.db.Query(ctx, `SELECT sha, name, zustand, adresse, text, zeit FROM commit_status
		WHERE repo=$1::uuid AND sha = ANY($2::text[]) ORDER BY name`, repo, shas)
	if err != nil {
		log.Printf("Commit-Status: %v", err)
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var p Pruefung
		if err := rows.Scan(&p.SHA, &p.Name, &p.Zustand, &p.Adresse, &p.Text, &p.Zeit); err != nil {
			log.Printf("Commit-Status: %v", err)
			return m
		}
		m[p.SHA] = append(m[p.SHA], p)
	}
	return m
}

func (a *App) statusMelden(w http.ResponseWriter, r *http.Request, besitzer, name, sha string) {
	fehler := func(code int, text string) { alsJSON(w, code, map[string]string{"fehler": text}) }
	if r.Method != http.MethodPost {
		fehler(http.StatusMethodNotAllowed, "nur POST")
		return
	}
	n, t, ok := r.BasicAuth()
	if !ok {
		fehler(http.StatusUnauthorized, "Benutzername und Zugriffstoken fehlen")
		return
	}
	wer, ok := a.tokenPruefen(r.Context(), n, t)
	if !ok {
		fehler(http.StatusUnauthorized, "Benutzername oder Zugriffstoken falsch")
		return
	}
	repo, err := a.repoLaden(r.Context(), besitzer, name)
	if err != nil {
		fehler(http.StatusNotFound, "nicht gefunden")
		return
	}
	switch a.rechtVon(r.Context(), repo, wer.ID) {
	case "schreiben":
	case "":
		fehler(http.StatusNotFound, "nicht gefunden")
		return
	default:
		fehler(http.StatusForbidden, "keine Schreibrechte")
		return
	}
	var e struct {
		Name    string `json:"name"`
		Zustand string `json:"zustand"`
		Adresse string `json:"adresse"`
		Text    string `json:"text"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&e) != nil {
		fehler(http.StatusBadRequest, "JSON unlesbar")
		return
	}
	if e.Name = strings.TrimSpace(e.Name); e.Name == "" {
		e.Name = "ci"
	}
	switch e.Zustand {
	case "pending", "success", "failure", "error":
	default:
		fehler(http.StatusBadRequest, "zustand: pending, success, failure oder error")
		return
	}
	if utf8.RuneCountInString(e.Name) > 60 || utf8.RuneCountInString(e.Text) > 300 || len(e.Adresse) > 500 || (e.Adresse != "" && herkunft(e.Adresse) == "") {
		fehler(http.StatusBadRequest, "Angaben zu lang oder Adresse nicht http(s)")
		return
	}
	if c, err := aufloesen(r.Context(), a.pfad(repo.Besitzer, repo.Name), sha); err != nil || c != sha {
		fehler(http.StatusNotFound, "diesen Commit gibt es nicht")
		return
	}
	if _, err := a.db.Exec(r.Context(), `INSERT INTO commit_status (repo, sha, name, zustand, adresse, text) VALUES ($1::uuid,$2,$3,$4,$5,$6)
		ON CONFLICT (repo, sha, name) DO UPDATE SET zustand=$4, adresse=$5, text=$6, zeit=now()`,
		repo.ID, sha, e.Name, e.Zustand, e.Adresse, e.Text); err != nil {
		log.Printf("Commit-Status: %v", err)
		fehler(http.StatusInternalServerError, "nicht gespeichert")
		return
	}
	alsJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
