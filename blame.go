package main

import (
	"context"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type BlameZeile struct {
	SHA, Kurz, Autor, Text string
	Zeit                   time.Time
	Nr                     int
	Erste                  bool // erste Zeile eines Blocks vom selben Commit
}

func blame(ctx context.Context, repo, ref, pfad string) ([]BlameZeile, error) {
	sha, err := aufloesen(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	// Die Revision ist jetzt ein Hash, der Pfad steht hinter --.
	aus, err := gitAus(ctx, repo, "blame", "--line-porcelain", sha, "--", pfad)
	if err != nil {
		return nil, err
	}
	var l []BlameZeile
	var z BlameZeile
	vorher := ""
	for _, s := range strings.Split(string(aus), "\n") {
		if text, ok := strings.CutPrefix(s, "\t"); ok {
			z.Text, z.Erste = strings.TrimSuffix(text, "\r"), z.SHA != vorher
			vorher = z.SHA
			l = append(l, z)
			z = BlameZeile{}
			continue
		}
		if z.SHA == "" {
			if f := strings.Fields(s); len(f) >= 3 && len(f[0]) >= 40 {
				z.SHA, z.Kurz = f[0], f[0][:8]
				if n, err := strconv.Atoi(f[2]); err == nil {
					z.Nr = n
				}
			}
			continue
		}
		if v, ok := strings.CutPrefix(s, "author "); ok {
			z.Autor = v
		} else if v, ok := strings.CutPrefix(s, "author-time "); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				z.Zeit = time.Unix(n, 0)
			}
		}
	}
	return l, nil
}

func (a *App) blameSeite(w http.ResponseWriter, r *http.Request) {
	repo, _, recht, ok := a.zugang(w, r)
	if !ok {
		return
	}
	p := a.pfad(repo.Besitzer, repo.Name)
	ref, pfad := refUndPfad(r.Context(), p, r.PathValue("rest"))
	if !sichererRef(ref) || !sichererPfad(pfad) || pfad == "" {
		http.NotFound(w, r)
		return
	}
	b, err := inhalt(r.Context(), p, ref, pfad)
	if err != nil || len(b) > 1<<20 || !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		http.Error(w, "Blame gibt es nur für Textdateien bis 1 MB", http.StatusUnprocessableEntity)
		return
	}
	l, err := blame(r.Context(), p, ref, pfad)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.v.zeigen(w, r, "blame", a.daten(r, "Blame "+path.Base(pfad)+" · "+repo.Voll(), "", map[string]any{"A": a.ansicht(r, repo, recht, ref, pfad), "Zeilen": l}))
}
