package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------- Aufrufe von git ----------

func (a *App) pfad(besitzer, name string) string {
	return filepath.Join(a.repos, besitzer, name+".git")
}

// gitAus führt git im Repository aus. Refs und Pfade gehen durch sichererRef
// und sichererPfad, Optionen enden vor ihnen mit --end-of-options.
func gitAus(ctx context.Context, repo string, args ...string) ([]byte, error) {
	ctx, abbruch := context.WithTimeout(ctx, 20*time.Second)
	defer abbruch()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "core.quotepath=off"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C.UTF-8")
	var fehler bytes.Buffer
	cmd.Stderr = &fehler
	aus, err := cmd.Output()
	if err != nil {
		return aus, fmt.Errorf("%v: %s", err, strings.TrimSpace(fehler.String()))
	}
	return aus, nil
}

var refMuster = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]{0,200}$`)

func sichererRef(ref string) bool {
	return refMuster.MatchString(ref) && !strings.Contains(ref, "..") && !strings.HasSuffix(ref, ".lock")
}

func sichererPfad(p string) bool {
	return !strings.Contains(p, "..") && !strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\x00\n")
}

func zweige(ctx context.Context, repo string) []string {
	aus, err := gitAus(ctx, repo, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	var l []string
	for _, z := range strings.Split(strings.TrimSpace(string(aus)), "\n") {
		if z != "" {
			l = append(l, z)
		}
	}
	return l
}

func standardzweig(ctx context.Context, repo string) string {
	l := zweige(ctx, repo)
	if aus, err := gitAus(ctx, repo, "symbolic-ref", "--short", "HEAD"); err == nil {
		z := strings.TrimSpace(string(aus))
		for _, b := range l {
			if b == z {
				return z
			}
		}
	}
	if len(l) > 0 {
		return l[0]
	}
	return "main"
}

type Eintrag struct {
	Name, Art, SHA, Pfad string
	Groesse              int64
}

// liste: Inhalt eines Verzeichnisses an einem Stand, Ordner zuerst.
func liste(ctx context.Context, repo, ref, pfad string) ([]Eintrag, error) {
	aus, err := gitAus(ctx, repo, "ls-tree", "-z", "-l", "--end-of-options", ref+":"+pfad)
	if err != nil {
		return nil, err
	}
	var ordner, dateien []Eintrag
	for _, z := range strings.Split(string(aus), "\x00") {
		tab := strings.IndexByte(z, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(z[:tab])
		if len(f) < 4 {
			continue
		}
		e := Eintrag{Name: z[tab+1:], Art: f[1], SHA: f[2]}
		e.Groesse, _ = strconv.ParseInt(f[3], 10, 64)
		e.Pfad = strings.TrimPrefix(pfad+"/"+e.Name, "/")
		if e.Art == "tree" {
			ordner = append(ordner, e)
		} else {
			dateien = append(dateien, e)
		}
	}
	return append(ordner, dateien...), nil
}

func inhalt(ctx context.Context, repo, ref, pfad string) ([]byte, error) {
	return gitAus(ctx, repo, "cat-file", "blob", "--end-of-options", ref+":"+pfad)
}

type Commit struct {
	SHA, Kurz, Autor, Email, Betreff, Text string
	Zeit                                   time.Time
}

func protokoll(ctx context.Context, repo, ref string, n int) ([]Commit, error) {
	aus, err := gitAus(ctx, repo, "log", "-z", "--format=%H%x1f%an%x1f%ae%x1f%at%x1f%s", "-n", strconv.Itoa(n), "--end-of-options", ref, "--")
	if err != nil {
		return nil, err
	}
	var l []Commit
	for _, z := range strings.Split(string(aus), "\x00") {
		f := strings.Split(strings.TrimSpace(z), "\x1f")
		if len(f) < 5 || len(f[0]) < 8 {
			continue
		}
		t, _ := strconv.ParseInt(f[3], 10, 64)
		l = append(l, Commit{SHA: f[0], Kurz: f[0][:8], Autor: f[1], Email: f[2], Zeit: time.Unix(t, 0), Betreff: f[4]})
	}
	return l, nil
}

type DiffZeile struct{ Klasse, Text string }

type DiffDatei struct {
	Name        string
	Plus, Minus int
	Zeilen      []DiffZeile
	Gekuerzt    bool
}

func commitMitDiff(ctx context.Context, repo, sha string) (Commit, []DiffDatei, error) {
	aus, err := gitAus(ctx, repo, "show", "--format=%H%x1f%an%x1f%ae%x1f%at%x1f%s%x1f%b%x1e", "--patch", "--no-color", "-M", "--end-of-options", sha, "--")
	if err != nil {
		return Commit{}, nil, err
	}
	kopf, rest, _ := bytes.Cut(aus, []byte("\x1e"))
	f := strings.SplitN(string(kopf), "\x1f", 6)
	if len(f) < 6 || len(f[0]) < 8 {
		return Commit{}, nil, errors.New("Commit unlesbar")
	}
	t, _ := strconv.ParseInt(f[3], 10, 64)
	c := Commit{SHA: f[0], Kurz: f[0][:8], Autor: f[1], Email: f[2], Zeit: time.Unix(t, 0), Betreff: f[4], Text: strings.TrimSpace(f[5])}
	var dateien []DiffDatei
	gesamt := 0
	sc := bufio.NewScanner(bytes.NewReader(rest))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		z := sc.Text()
		if strings.HasPrefix(z, "diff --git ") {
			dateien = append(dateien, DiffDatei{Name: diffName(z)})
			continue
		}
		if len(dateien) == 0 || strings.HasPrefix(z, "index ") || strings.HasPrefix(z, "--- ") || strings.HasPrefix(z, "+++ ") {
			continue
		}
		akt := &dateien[len(dateien)-1]
		k := ""
		switch {
		case strings.HasPrefix(z, "@@"):
			k = "abschnitt"
		case strings.HasPrefix(z, "+"):
			k = "plus"
			akt.Plus++
		case strings.HasPrefix(z, "-"):
			k = "minus"
			akt.Minus++
		}
		gesamt++
		if len(akt.Zeilen) < 2000 && gesamt < 20000 {
			akt.Zeilen = append(akt.Zeilen, DiffZeile{k, z})
		} else {
			akt.Gekuerzt = true
		}
	}
	return c, dateien, nil
}

func diffName(z string) string {
	if i := strings.LastIndex(z, " b/"); i > 0 {
		return z[i+3:]
	}
	return strings.TrimPrefix(z, "diff --git ")
}

// ---------- git über HTTPS ----------

// git beantwortet clone, fetch und push über git-http-backend. Lesen geht bei
// öffentlichen Repositories ohne Anmeldung, sonst und zum Schreiben mit
// Benutzername und Zugriffstoken (Basic Auth).
func (a *App) git(w http.ResponseWriter, r *http.Request, besitzer, name, rest string) {
	verweigern := func(code int, text string) {
		if code == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="Forgr"`)
		}
		http.Error(w, text, code)
	}
	switch rest {
	case "/info/refs", "/git-upload-pack", "/git-receive-pack", "/HEAD":
	default:
		verweigern(http.StatusNotFound, "nicht gefunden")
		return
	}
	var wer Konto
	if n, t, ok := r.BasicAuth(); ok {
		k, gut := a.tokenPruefen(r.Context(), n, t)
		if !gut {
			verweigern(http.StatusUnauthorized, "Benutzername oder Zugriffstoken falsch")
			return
		}
		wer = k
	}
	// Ohne Token kein git: auch öffentliche Repos sind nur für Konten
	// sichtbar, wie in der Weboberfläche.
	if wer.ID == "" {
		verweigern(http.StatusUnauthorized, "Bitte mit Benutzername und Zugriffstoken anmelden")
		return
	}
	repo, err := a.repoLaden(r.Context(), besitzer, name)
	recht := ""
	if err == nil {
		recht = a.rechtVon(r.Context(), repo, wer.ID)
	}
	schreiben := rest == "/git-receive-pack" || r.URL.Query().Get("service") == "git-receive-pack"
	if recht == "" || (schreiben && recht != "schreiben") {
		// Ohne Anmeldung sieht ein privates Repository aus wie keines.
		if wer.ID == "" {
			verweigern(http.StatusUnauthorized, "Bitte mit Benutzername und Zugriffstoken anmelden")
		} else if recht == "" {
			verweigern(http.StatusNotFound, "nicht gefunden")
		} else {
			verweigern(http.StatusForbidden, "keine Schreibrechte")
		}
		return
	}
	backend := "/usr/libexec/git-core/git-http-backend"
	if aus, err := exec.Command("git", "--exec-path").Output(); err == nil {
		backend = filepath.Join(strings.TrimSpace(string(aus)), "git-http-backend")
	}
	h := &cgi.Handler{
		Path: backend,
		Dir:  a.repos,
		Env: []string{
			"GIT_PROJECT_ROOT=" + a.repos,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_HTTP_MAX_REQUEST_BUFFER=100M",
			"REMOTE_USER=" + wer.Name,
			"HOME=" + os.Getenv("HOME"),
		},
	}
	if schreiben {
		defer a.db.Exec(r.Context(), `UPDATE repos SET geaendert=now() WHERE id::text=$1`, repo.ID)
	}
	h.ServeHTTP(w, r)
}
