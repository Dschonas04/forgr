package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Konto struct {
	ID, Kennung, Name, Anzeige, Email string
}

var kontoName = regexp.MustCompile(`[^a-z0-9._-]+`)

// ich legt das Konto der angemeldeten Person an. Der Name in Adressen kommt
// aus dem Nutzernamen beim Anbieter; ist er vergeben, mit Zahl dahinter.
func (a *App) ich(r *http.Request) (Konto, error) {
	n, ok := a.sso.Wer(r)
	if !ok && !a.sso.notaus {
		return Konto{}, errors.New("nicht angemeldet")
	}
	kennung := n.Kennung
	if kennung == "" {
		kennung = "lokal:" + n.Nutzer
	}
	var k Konto
	err := a.db.QueryRow(r.Context(), `UPDATE konten SET anzeige=$2, email=$3 WHERE kennung=$1
		RETURNING id::text, kennung, name, anzeige, email`, kennung, n.Name, n.Email).
		Scan(&k.ID, &k.Kennung, &k.Name, &k.Anzeige, &k.Email)
	if err == nil {
		return k, nil
	}
	grund := strings.Trim(kontoName.ReplaceAllString(strings.ToLower(n.Nutzer), "-"), "-.")
	if grund == "" {
		grund = strings.Trim(kontoName.ReplaceAllString(strings.ToLower(strings.Split(n.Email, "@")[0]), "-"), "-.")
	}
	if grund == "" || !gueltigerName(grund) {
		grund = "konto"
	}
	name := grund
	for i := 2; i < 100; i++ {
		err = a.db.QueryRow(r.Context(), `INSERT INTO konten (kennung, name, anzeige, email) VALUES ($1, $2, $3, $4)
			ON CONFLICT (kennung) DO UPDATE SET anzeige=$3
			RETURNING id::text, kennung, name, anzeige, email`, kennung, name, n.Name, n.Email).
			Scan(&k.ID, &k.Kennung, &k.Name, &k.Anzeige, &k.Email)
		if err == nil {
			return k, nil
		}
		if !strings.Contains(err.Error(), "konten_name_key") {
			return k, err
		}
		name = grund + "-" + itoa(i)
	}
	return k, err
}

func (a *App) kontoNamens(ctx context.Context, name string) (Konto, error) {
	var k Konto
	err := a.db.QueryRow(ctx, `SELECT id::text, kennung, name, anzeige, email FROM konten WHERE name=$1`, name).
		Scan(&k.ID, &k.Kennung, &k.Name, &k.Anzeige, &k.Email)
	return k, err
}

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// tokenPruefen: Benutzername und Token aus Basic Auth.
func (a *App) tokenPruefen(ctx context.Context, name, token string) (Konto, bool) {
	var k Konto
	var id string
	err := a.db.QueryRow(ctx, `SELECT t.id::text, k.id::text, k.kennung, k.name, k.anzeige, k.email
		FROM tokens t JOIN konten k ON k.id = t.konto WHERE t.hash = $1 AND k.name = $2`, tokenHash(token), name).
		Scan(&id, &k.ID, &k.Kennung, &k.Name, &k.Anzeige, &k.Email)
	if err != nil {
		return Konto{}, false
	}
	a.db.Exec(ctx, `UPDATE tokens SET zuletzt=now() WHERE id::text=$1 AND (zuletzt IS NULL OR zuletzt < now() - interval '1 minute')`, id)
	return k, true
}

type Token struct {
	ID, Name, Anfang string
	Erstellt         time.Time
	Zuletzt          *time.Time
}

func (a *App) tokens(w http.ResponseWriter, r *http.Request) {
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.tokensZeigen(w, r, ich, "")
}

func (a *App) tokensZeigen(w http.ResponseWriter, r *http.Request, ich Konto, neu string) {
	rows, err := a.db.Query(r.Context(), `SELECT id::text, name, anfang, erstellt, zuletzt FROM tokens WHERE konto=$1::uuid ORDER BY erstellt DESC`, ich.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var l []Token
	for rows.Next() {
		var t Token
		if rows.Scan(&t.ID, &t.Name, &t.Anfang, &t.Erstellt, &t.Zuletzt) == nil {
			l = append(l, t)
		}
	}
	a.v.zeigen(w, r, "tokens", a.daten(r, "Zugriffstoken", "tokens", map[string]any{"Tokens": l, "Neu": neu, "Ich": ich, "Basis": basis(r)}))
}

func (a *App) tokenAnlegen(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	ich, err := a.ich(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Token"
	}
	b := make([]byte, 24)
	rand.Read(b)
	t := "fgr_" + hex.EncodeToString(b)
	if _, err := a.db.Exec(r.Context(), `INSERT INTO tokens (konto, name, hash, anfang) VALUES ($1::uuid, $2, $3, $4)`,
		ich.ID, name, tokenHash(t), t[:8]); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Das Token steht nur in dieser einen Antwort.
	w.Header().Set("Cache-Control", "no-store")
	a.tokensZeigen(w, r, ich, t)
}

func (a *App) tokenLoeschen(w http.ResponseWriter, r *http.Request) {
	if !pruefeFormular(w, r) {
		return
	}
	ich, _ := a.ich(r)
	a.db.Exec(r.Context(), `DELETE FROM tokens WHERE id::text=$1 AND konto=$2::uuid`, r.PathValue("id"), ich.ID)
	zurueck(w, r, "/tokens", "Token widerrufen")
}
