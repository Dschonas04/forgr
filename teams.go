package main

import (
	"context"
	"net/http"
	"strings"
)

// Eine Gruppe gilt nur an den Repos dessen, dem sie gehört. Sonst öffnete, wer
// die Mitglieder bestimmt, fremde private Repos.

type Team struct {
	ID, Name, Recht string
	Mitglieder      []string
}

func (a *App) teamsVon(ctx context.Context, konto, repo string) ([]Team, error) {
	rows, err := a.db.Query(ctx, `SELECT t.id::text, t.name,
		COALESCE((SELECT x.recht FROM team_rechte x WHERE x.team = t.id AND x.repo::text = $2), ''),
		COALESCE((SELECT array_agg(k.name ORDER BY k.name) FROM team_mitglieder m JOIN konten k ON k.id = m.konto WHERE m.team = t.id), '{}')
		FROM teams t WHERE t.besitzer = $1::uuid ORDER BY t.name`, konto, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var l []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.Recht, &t.Mitglieder); err != nil {
			return nil, err
		}
		l = append(l, t)
	}
	return l, rows.Err()
}

func (a *App) gruppenSeite(w http.ResponseWriter, r *http.Request) {
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	l, err := a.teamsVon(r.Context(), ich.ID, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.v.zeigen(w, r, "gruppen", a.daten(r, "Gruppen", "gruppen", map[string]any{"Teams": l}))
}

func (a *App) gruppeAendern(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	const hier = "/gruppen"
	id, name := r.FormValue("id"), strings.TrimSpace(r.FormValue("name"))
	if id != "" {
		var meine bool
		if err := a.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM teams WHERE id::text=$1 AND besitzer=$2::uuid)`, id, ich.ID).Scan(&meine); err != nil || !meine {
			http.NotFound(w, r)
			return
		}
	}
	switch aktion := r.FormValue("aktion"); {
	case id == "":
		if name == "" || len(name) > 60 {
			zurueck(w, r, hier, "Name fehlt oder ist zu lang")
			return
		}
		_, err = a.db.Exec(r.Context(), `INSERT INTO teams (besitzer, name) VALUES ($1::uuid, $2)`, ich.ID, name)
	case aktion == "loeschen":
		_, err = a.db.Exec(r.Context(), `DELETE FROM teams WHERE id::text=$1`, id)
	case aktion == "dazu" || aktion == "weg":
		k, e := a.kontoNamens(r.Context(), name)
		if e != nil {
			zurueck(w, r, hier, "Dieses Konto gibt es nicht. Es entsteht bei der ersten Anmeldung.")
			return
		}
		q := `INSERT INTO team_mitglieder (team, konto) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`
		if aktion == "weg" {
			q = `DELETE FROM team_mitglieder WHERE team=$1::uuid AND konto=$2::uuid`
		}
		_, err = a.db.Exec(r.Context(), q, id, k.ID)
	default:
		zurueck(w, r, hier, "Unbekannte Aktion")
		return
	}
	if err != nil {
		zurueck(w, r, hier, "Nicht gespeichert")
		return
	}
	zurueck(w, r, hier, "Gespeichert")
}

func (a *App) darfVerwalten(ctx context.Context, r Repo, konto string) bool {
	return konto == r.BesitzerID || a.hoechstesRecht(ctx, r, konto) == "verwalten"
}

func (a *App) teamRecht(w http.ResponseWriter, r *http.Request) {
	// Gruppen gehören dem Besitzer, also vergibt auch nur er sie.
	repo, ok := a.besitzerFormular(w, r)
	if !ok {
		return
	}
	hier, recht := "/"+repo.Voll()+"/einstellungen", r.FormValue("recht")
	var err error
	switch recht {
	case "lesen", "schreiben", "verwalten":
		_, err = a.db.Exec(r.Context(), `INSERT INTO team_rechte (repo, team, recht)
			SELECT $1::uuid, t.id, $3 FROM teams t WHERE t.id::text=$2 AND t.besitzer=$4::uuid
			ON CONFLICT (repo, team) DO UPDATE SET recht=$3`, repo.ID, r.FormValue("team"), recht, repo.BesitzerID)
	default:
		_, err = a.db.Exec(r.Context(), `DELETE FROM team_rechte WHERE repo=$1::uuid AND team::text=$2`, repo.ID, r.FormValue("team"))
	}
	if err != nil {
		zurueck(w, r, hier, "Nicht gespeichert")
		return
	}
	zurueck(w, r, hier, "Gespeichert")
}
