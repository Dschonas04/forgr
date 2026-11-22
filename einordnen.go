package main

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

func labelListe(s string) []string {
	l := []string{}
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e == "" || len([]rune(e)) > 40 {
			continue
		}
		if !slices.Contains(l, e) && len(l) < 10 {
			l = append(l, e)
		}
	}
	return l
}

type Meilenstein struct {
	Name           string
	Fertig, Gesamt int
}

func (a *App) meilensteine(ctx context.Context, repo string) ([]Meilenstein, error) {
	rows, err := a.db.Query(ctx, `SELECT meilenstein, count(*) FILTER (WHERE NOT offen), count(*)
		FROM issues WHERE repo=$1::uuid AND meilenstein <> '' GROUP BY 1 ORDER BY 1`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var l []Meilenstein
	for rows.Next() {
		var m Meilenstein
		if err := rows.Scan(&m.Name, &m.Fertig, &m.Gesamt); err != nil {
			return nil, err
		}
		l = append(l, m)
	}
	return l, rows.Err()
}

func (a *App) issueEinordnen(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	if recht != "schreiben" {
		http.Error(w, "Einordnen braucht Schreibrecht", http.StatusForbidden)
		return
	}
	i, err := a.issueLaden(r, repo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ms := strings.TrimSpace(r.FormValue("meilenstein"))
	if z := []rune(ms); len(z) > 60 {
		ms = string(z[:60])
	}
	hier := "/" + repo.Voll() + "/issues/" + strconv.Itoa(i.Nummer)
	if _, err := a.db.Exec(r.Context(), `UPDATE issues SET labels=$2, meilenstein=$3, geaendert=now() WHERE id::text=$1`,
		i.ID, labelListe(r.FormValue("labels")), ms); err != nil {
		zurueck(w, r, hier, "Nicht gespeichert")
		return
	}
	zurueck(w, r, hier, "Eingeordnet")
}
