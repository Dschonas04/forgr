package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"log"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Spiegel legt der Betreiber in FORGR_SPIEGEL fest, nie die Oberfläche. Sie
// überschreiben und löschen nichts: kein Force, kein Prune.

type Spiegel struct {
	Besitzer, Name, Richtung, Adresse string
}

func spiegelListe(s string) []Spiegel {
	var l []Spiegel
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		f := strings.Fields(e)
		if len(f) != 3 || (f[1] != "push" && f[1] != "pull") {
			log.Printf("spiegel: Eintrag unlesbar, übergangen (besitzer/repo push|pull https://...)")
			continue
		}
		b, n, ok := strings.Cut(f[0], "/")
		u, err := url.Parse(f[2])
		if !ok || !gueltigerName(b) || !gueltigerName(n) || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			log.Printf("spiegel: %q braucht besitzer/repo und https ohne Zugangsdaten, übergangen", f[0])
			continue
		}
		l = append(l, Spiegel{b, n, f[1], f[2]})
	}
	return l
}

func (s Spiegel) befehl() []string {
	refs := []string{s.Adresse, "refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*"}
	if s.Richtung == "pull" {
		return slices.Concat([]string{"fetch", "--porcelain", "--no-auto-maintenance", "--no-write-fetch-head", "--no-prune", "--no-tags", "--"}, refs)
	}
	return slices.Concat([]string{"push", "--"}, refs)
}

// spiegelUmgebung gibt das Token nur an seinen Host und nie in die Argumente.
func spiegelUmgebung(s Spiegel, host, token string) []string {
	env := gitUmgebung()
	u, err := url.Parse(s.Adresse)
	if err != nil || token == "" || host == "" || !strings.EqualFold(u.Host, host) {
		return env
	}
	return append(env, "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://"+u.Host+"/.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
}

func (a *App) spiegelLauf(ctx context.Context, l []Spiegel, host, token string) {
	if len(l) == 0 {
		return
	}
	takt := time.NewTicker(10 * time.Minute)
	defer takt.Stop()
	for {
		for _, s := range l {
			a.spiegeln(ctx, s, host, token)
		}
		select {
		case <-ctx.Done():
			return
		case <-takt.C:
		}
	}
}

func (a *App) spiegeln(ctx context.Context, s Spiegel, host, token string) {
	ctx, ende := context.WithTimeout(ctx, 5*time.Minute)
	defer ende()
	repo, err := a.repoLaden(ctx, s.Besitzer, s.Name)
	if err != nil {
		log.Printf("spiegel %s/%s: Repository fehlt", s.Besitzer, s.Name)
		return
	}
	p := a.pfad(repo.Besitzer, repo.Name)
	cmd := exec.CommandContext(ctx, "git", slices.Concat([]string{"-C", p,
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false", "-c", "credential.helper=",
	}, s.befehl())...)
	cmd.Env = spiegelUmgebung(s, host, token)
	var fehler bytes.Buffer
	cmd.Stderr = &fehler
	aus, err := cmd.Output()
	if err != nil {
		log.Printf("spiegel %s %s: %s", repo.Voll(), s.Richtung, kurz(fehler.String()))
	}
	// Auch nach einem Fehler: was durchkam, steht in der Ausgabe.
	if s.Richtung == "pull" {
		if l := fetchRefs(aus); len(l) > 0 {
			a.melden(repo, "push", "spiegel", map[string]any{"refs": l})
		}
	}
}
