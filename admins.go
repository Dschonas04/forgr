package main

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
)

// adminListe liest die Vorgabe des Betreibers: Kennungen mit Komma getrennt,
// konto:<name> oder oidc:<Aussteller>|<sub>.
func adminListe(s string) []string {
	var l []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			l = append(l, e)
		}
	}
	return l
}

// istAdmin liest nur. Admin ist, wen der Betreiber vorgibt oder wer die App mit
// dem Code aus dem Log eingerichtet hat. Im Notaus ist die Sperre ohnehin offen.
func (a *App) istAdmin(r *http.Request) bool {
	if a.sso.notaus {
		return true
	}
	n, ok := a.sso.Wer(r)
	if !ok {
		return false
	}
	if slices.Contains(a.admins, n.Kennung) {
		return true
	}
	var ja bool
	if err := a.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM admins WHERE kennung = $1)`, n.Kennung).Scan(&ja); err != nil {
		log.Printf("admins: %v", err)
		return false
	}
	return ja
}

// ersterAdmin läuft, wenn mit dem Einrichtungscode das erste Konto entsteht.
func (a *App) ersterAdmin(ctx context.Context, kennung, _ string) error {
	_, err := a.db.Exec(ctx, `INSERT INTO admins (kennung) VALUES ($1) ON CONFLICT DO NOTHING`, kennung)
	return err
}

// adminEntziehen läuft vor dem Löschen eines Kontos und vor dem Anlegen eines
// neuen mit demselben Namen: Token enden, Repos gehen nicht an den Namen über.
func (a *App) adminEntziehen(ctx context.Context, kennung string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		`DELETE FROM admins WHERE kennung = $1`,
		`DELETE FROM tokens WHERE konto IN (SELECT id FROM konten WHERE kennung = $1)`,
		`UPDATE konten SET kennung = 'geloescht:' || id::text WHERE kennung = $1`,
	} {
		if _, err := tx.Exec(ctx, q, kennung); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
