package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Webhooks gehen nur an Hosts aus FORGR_WEBHOOK_ZIELE. Sonst ließe, wer ein
// Repo hat, den Server beliebige Adressen im Netz anrufen.

type Webhook struct {
	ID, Adresse, Stand string
	Geheimnis          string // nur für die Einstellungen
	Zuletzt            *time.Time
}

type Hooks struct {
	ziele   []string
	geheim  string
	plaetze chan struct{}
	klient  *http.Client
}

func herkunft(roh string) string {
	u, err := url.Parse(strings.TrimSpace(roh))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	// Nur ASCII: ToLower und der Transport normalisieren sonst verschieden, und
	// geprüft wäre ein anderer Host als der angerufene.
	for _, b := range []byte(u.Host) {
		if b <= ' ' || b >= 0x7f || b == '%' {
			return ""
		}
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func neueHooks(ziele, geheim string) *Hooks {
	h := &Hooks{geheim: geheim, plaetze: make(chan struct{}, 8), klient: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	for _, z := range strings.Split(ziele, ",") {
		if o := herkunft(z); o != "" {
			h.ziele = append(h.ziele, o)
		}
	}
	return h
}

func (h *Hooks) erlaubt(adresse string) bool {
	if h == nil {
		return false
	}
	o := herkunft(adresse)
	return o != "" && slices.Contains(h.ziele, o)
}

func (h *Hooks) an() bool { return h != nil && len(h.ziele) > 0 }

// schluessel leitet je Hook einen eigenen ab: wer einen Hook verwaltet, kann
// damit keine Meldung eines anderen unterschreiben.
func (h *Hooks) schluessel(id string) string {
	if h == nil || h.geheim == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(h.geheim))
	m.Write([]byte("forgr-webhook:" + id))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *App) webhooksVon(ctx context.Context, repo string) ([]Webhook, error) {
	rows, err := a.db.Query(ctx, `SELECT id::text, adresse, stand, zuletzt FROM webhooks WHERE repo=$1::uuid ORDER BY adresse`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var l []Webhook
	for rows.Next() {
		var x Webhook
		if err := rows.Scan(&x.ID, &x.Adresse, &x.Stand, &x.Zuletzt); err != nil {
			return nil, err
		}
		l = append(l, x)
	}
	return l, rows.Err()
}

// melden ruft die Hooks eines Repos im Hintergrund.
func (a *App) melden(repo Repo, ereignis, wer string, daten map[string]any) {
	if !a.hooks.an() {
		return
	}
	select {
	case a.hooks.plaetze <- struct{}{}:
	default:
		log.Printf("webhook %s: zu viele gleichzeitig, übergangen", repo.Voll())
		return
	}
	go func() {
		defer func() { <-a.hooks.plaetze }()
		ctx, ende := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ende()
		l, err := a.webhooksVon(ctx, repo.ID)
		if err != nil {
			log.Printf("webhook %s: %v", repo.Voll(), err)
			return
		}
		b, err := json.Marshal(map[string]any{"ereignis": ereignis, "repo": repo.Voll(), "konto": wer, "zeit": time.Now().UTC(), "daten": daten})
		if err != nil {
			log.Printf("webhook %s: %v", repo.Voll(), err)
			return
		}
		for _, x := range l {
			stand := a.hooks.senden(ctx, x, ereignis, b)
			if _, err := a.db.Exec(ctx, `UPDATE webhooks SET stand=$2, zuletzt=now() WHERE id::text=$1`, x.ID, stand); err != nil {
				log.Printf("webhook %s: %v", repo.Voll(), err)
			}
		}
	}()
}

func (h *Hooks) senden(ctx context.Context, x Webhook, ereignis string, b []byte) string {
	adresse := x.Adresse
	if !h.erlaubt(adresse) {
		return "Ziel nicht erlaubt"
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, adresse, bytes.NewReader(b))
	if err != nil {
		return "Adresse ungültig"
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forgr-Ereignis", ereignis)
	if k := h.schluessel(x.ID); k != "" {
		m := hmac.New(sha256.New, []byte(k))
		m.Write(b)
		r.Header.Set("X-Forgr-Signatur", "sha256="+hex.EncodeToString(m.Sum(nil)))
	}
	res, err := h.klient.Do(r)
	if err != nil {
		return "nicht erreichbar"
	}
	defer res.Body.Close()
	return fmt.Sprintf("HTTP %d", res.StatusCode)
}

func (a *App) webhookAendern(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.verwalterFormular(w, r)
	if !ok {
		return
	}
	hier := "/" + repo.Voll() + "/einstellungen"
	var err error
	if id := r.FormValue("id"); id != "" {
		_, err = a.db.Exec(r.Context(), `DELETE FROM webhooks WHERE id::text=$1 AND repo=$2::uuid`, id, repo.ID)
	} else {
		z := strings.TrimSpace(r.FormValue("adresse"))
		if !a.hooks.erlaubt(z) {
			zurueck(w, r, hier, "Dieses Ziel ist nicht freigegeben (FORGR_WEBHOOK_ZIELE)")
			return
		}
		_, err = a.db.Exec(r.Context(), `INSERT INTO webhooks (repo, adresse)
			SELECT $1::uuid, $2 WHERE (SELECT count(*) FROM webhooks WHERE repo=$1::uuid) < 10`, repo.ID, z)
	}
	if err != nil {
		zurueck(w, r, hier, "Nicht gespeichert")
		return
	}
	zurueck(w, r, hier, "Gespeichert")
}
