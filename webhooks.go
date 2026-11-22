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
			stand := a.hooks.senden(ctx, x.Adresse, ereignis, b)
			if _, err := a.db.Exec(ctx, `UPDATE webhooks SET stand=$2, zuletzt=now() WHERE id::text=$1`, x.ID, stand); err != nil {
				log.Printf("webhook %s: %v", repo.Voll(), err)
			}
		}
	}()
}

func (h *Hooks) senden(ctx context.Context, adresse, ereignis string, b []byte) string {
	if !h.erlaubt(adresse) {
		return "Ziel nicht erlaubt"
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, adresse, bytes.NewReader(b))
	if err != nil {
		return "Adresse ungültig"
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forgr-Ereignis", ereignis)
	if h.geheim != "" {
		m := hmac.New(sha256.New, []byte(h.geheim))
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

// refStand: jeder Zweig und Tag mit seinem Objekt, zum Vergleich um einen Push.
func refStand(ctx context.Context, repo string) (map[string]string, error) {
	aus, err := gitAus(ctx, repo, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, z := range strings.Split(string(aus), "\n") {
		if sha, ref, ok := strings.Cut(z, " "); ok {
			m[ref] = sha
		}
	}
	return m, nil
}

func refAenderungen(vor, nach map[string]string) []map[string]string {
	var l []map[string]string
	for ref, neu := range nach {
		if vor[ref] != neu {
			l = append(l, map[string]string{"ref": ref, "vorher": vor[ref], "nachher": neu})
		}
	}
	for ref, alt := range vor {
		if _, da := nach[ref]; !da {
			l = append(l, map[string]string{"ref": ref, "vorher": alt, "nachher": ""})
		}
	}
	slices.SortFunc(l, func(x, y map[string]string) int { return strings.Compare(x["ref"], y["ref"]) })
	return l
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
