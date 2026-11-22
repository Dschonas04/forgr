package main

import (
	"context"
	"html/template"
	"log"
	"mime"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Release struct {
	Tag, SHA, Titel string
	HTML            template.HTML
	Zeit            time.Time
	Notizen         bool
}

func (a *App) releases(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	p := a.pfad(repo.Besitzer, repo.Name)
	aus, err := gitAus(r.Context(), p, "for-each-ref", "--sort=-creatordate", "--format=%(refname)%1f%(objectname)%1f%(*objectname)%1f%(creatordate:unix)", "refs/tags")
	if err != nil {
		http.Error(w, "Tags nicht lesbar", 500)
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT tag, titel, text FROM releases WHERE repo=$1::uuid`, repo.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	notiz := map[string][2]string{}
	for rows.Next() {
		var tag, titel, text string
		if err := rows.Scan(&tag, &titel, &text); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		notiz[tag] = [2]string{titel, text}
	}
	var l []Release
	for _, z := range strings.Split(strings.TrimSpace(string(aus)), "\n") {
		f := strings.Split(z, "\x1f")
		if len(f) < 4 {
			continue
		}
		x := Release{Tag: strings.TrimPrefix(f[0], "refs/tags/"), SHA: f[1]}
		if f[2] != "" {
			x.SHA = f[2] // annotierter Tag: der Commit dahinter
		}
		x.Titel = x.Tag
		if t, err := strconv.ParseInt(f[3], 10, 64); err == nil {
			x.Zeit = time.Unix(t, 0)
		}
		if n, da := notiz[x.Tag]; da {
			x.Titel, x.HTML, x.Notizen = n[0], mdSicher(n[1]), true
		}
		l = append(l, x)
	}
	a.v.zeigen(w, r, "releases", a.daten(r, "Releases · "+repo.Voll(), "", map[string]any{"A": a.ansicht(r, repo, recht, "", ""), "Liste": l}))
}

func (a *App) releaseSpeichern(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	if recht != "schreiben" {
		http.Error(w, "Releases brauchen Schreibrecht", http.StatusForbidden)
		return
	}
	hier := "/" + repo.Voll() + "/releases"
	p, tag := a.pfad(repo.Besitzer, repo.Name), strings.TrimSpace(r.FormValue("tag"))
	if !sichererRef(tag) {
		zurueck(w, r, hier, "Tagname ungültig")
		return
	}
	if _, err := gitAus(r.Context(), p, "check-ref-format", "refs/tags/"+tag); err != nil {
		zurueck(w, r, hier, "Tagname ungültig")
		return
	}
	if _, err := gitAus(r.Context(), p, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag); err != nil {
		sha, err := zweigStand(r.Context(), p, r.FormValue("zweig"))
		if err != nil {
			zurueck(w, r, hier, "Diesen Zweig gibt es nicht")
			return
		}
		// Leerer alter Wert: nie einen vorhandenen Tag verschieben.
		if _, err := gitAus(r.Context(), p, "update-ref", "refs/tags/"+tag, sha, ""); err != nil {
			zurueck(w, r, hier, "Tag nicht angelegt")
			return
		}
	}
	titel := strings.TrimSpace(r.FormValue("titel"))
	if titel == "" {
		titel = tag
	}
	if _, err := a.db.Exec(r.Context(), `INSERT INTO releases (repo, tag, titel, text) VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (repo, tag) DO UPDATE SET titel=$3, text=$4`, repo.ID, tag, titel, strings.TrimSpace(r.FormValue("text"))); err != nil {
		zurueck(w, r, hier, "Nicht gespeichert")
		return
	}
	zurueck(w, r, hier, "Release gespeichert")
}

// archiv streamt einen Tag als tar.gz.
func (a *App) archiv(w http.ResponseWriter, r *http.Request) {
	repo, _, _, ok := a.zugang(w, r)
	if !ok {
		return
	}
	tag, p := r.PathValue("tag"), a.pfad(repo.Besitzer, repo.Name)
	if !sichererRef(tag) {
		http.NotFound(w, r)
		return
	}
	aus, err := gitAus(r.Context(), p, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	stamm := repo.Name + "-" + strings.ReplaceAll(tag, "/", "-")
	ctx, ende := context.WithTimeout(r.Context(), 5*time.Minute)
	defer ende()
	cmd := exec.CommandContext(ctx, "git", "-C", p, "archive", "--format=tar.gz", "--prefix="+stamm+"/", strings.TrimSpace(string(aus)))
	cmd.Env = gitUmgebung()
	cmd.Stdout = w
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": stamm + ".tar.gz"}))
	if err := cmd.Run(); err != nil {
		log.Printf("archiv %s: %v", repo.Voll(), err)
	}
}
