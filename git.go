package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	cmd.Env = gitUmgebung()
	var fehler bytes.Buffer
	cmd.Stderr = &fehler
	rohr, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	aus, lesen := io.ReadAll(io.LimitReader(rohr, gitMax+1))
	if lesen != nil || len(aus) > gitMax {
		abbruch()
		_ = cmd.Wait() // das Ende ist erzwungen, der Grund steht schon fest
		if lesen != nil {
			return nil, lesen
		}
		return nil, errors.New("Ausgabe von git zu groß")
	}
	if err := cmd.Wait(); err != nil {
		return aus, fmt.Errorf("%v: %s", err, strings.TrimSpace(fehler.String()))
	}
	return aus, nil
}

// mehr liest die App von git nicht in den Speicher
const gitMax = 32 << 20

var refMuster = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]{0,200}$`)

func sichererRef(ref string) bool {
	return refMuster.MatchString(ref) && !strings.Contains(ref, "..") && !strings.HasSuffix(ref, ".lock")
}

func sichererPfad(p string) bool {
	return !strings.Contains(p, "..") && !strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\x00\n")
}

func zweige(ctx context.Context, repo string) []string {
	aus, err := gitAus(ctx, repo, "for-each-ref", "--sort=-committerdate", "--format=%(refname:strip=2)", "refs/heads")
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
	if aus, err := gitAus(ctx, repo, "symbolic-ref", "HEAD"); err == nil {
		z := strings.TrimPrefix(strings.TrimSpace(string(aus)), "refs/heads/")
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
	ref, err := aufloesen(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
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
	ref, err := aufloesen(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	return gitAus(ctx, repo, "cat-file", "blob", "--end-of-options", ref+":"+pfad)
}

type Commit struct {
	SHA, Kurz, Autor, Email, Betreff, Text string
	Zeit                                   time.Time
}

func protokoll(ctx context.Context, repo, ref string, n int) ([]Commit, error) {
	ref, err := aufloesen(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
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

type DiffZeile struct {
	Klasse, Text string
	Neu          int // Zeilennummer im neuen Stand, 0 bei entfernten
}

type DiffDatei struct {
	Name        string
	Plus, Minus int
	Zeilen      []DiffZeile
	Gekuerzt    bool
}

func commitMitDiff(ctx context.Context, repo, sha string) (Commit, []DiffDatei, error) {
	sha, err := aufloesen(ctx, repo, sha)
	if err != nil {
		return Commit{}, nil, err
	}
	// Nachricht und Diff getrennt holen: in der Nachricht kann jedes Trennzeichen stehen.
	aus, err := gitAus(ctx, repo, "show", "-s", "--format=%H%x1f%at%x1f%an%x1f%ae%x1f%s%x1f%b", "--end-of-options", sha, "--")
	if err != nil {
		return Commit{}, nil, err
	}
	rest, err := gitAus(ctx, repo, "show", "--format=", "--no-ext-diff", "--no-textconv", "--patch", "--no-color", "-M", "--end-of-options", sha, "--")
	if err != nil {
		return Commit{}, nil, err
	}
	f := strings.SplitN(string(aus), "\x1f", 6)
	if len(f) < 6 || len(f[0]) < 8 {
		return Commit{}, nil, errors.New("Commit unlesbar")
	}
	f[1], f[2], f[3] = f[2], f[3], f[1]
	t, _ := strconv.ParseInt(f[3], 10, 64)
	c := Commit{SHA: f[0], Kurz: f[0][:8], Autor: f[1], Email: f[2], Zeit: time.Unix(t, 0), Betreff: f[4], Text: strings.TrimSpace(f[5])}
	return c, diffLesen(rest), nil
}

var abschnittKopf = regexp.MustCompile(`^@@ .*?\+(\d+)(?:,\d+)? @@`)

// diffZwischen zeigt, was das Zusammenführen am Ziel ändert: gegen denselben
// Baum, den der Merge schreibt. Nur bei Konflikt, wo es keinen Merge gibt,
// bleibt der Vergleich mit dem gemeinsamen Anfang.
func diffZwischen(ctx context.Context, repo, ziel, quelle string) ([]DiffDatei, error) {
	staende := []string{ziel + "..." + quelle}
	if baum, err := mergeBaum(ctx, repo, ziel, quelle); err == nil {
		staende = []string{ziel, baum}
	}
	args := slices.Concat([]string{"diff", "--patch", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--end-of-options"}, staende, []string{"--"})
	aus, err := gitAus(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	return diffLesen(aus), nil
}

func diffLesen(roh []byte) []DiffDatei {
	var dateien []DiffDatei
	gesamt, neu := 0, 0
	imAbschnitt := false
	sc := bufio.NewScanner(bytes.NewReader(roh))
	sc.Buffer(make([]byte, 1<<20), gitMax)
	for sc.Scan() {
		z := sc.Text()
		if strings.HasPrefix(z, "diff --git ") {
			dateien = append(dateien, DiffDatei{Name: diffName(z)})
			imAbschnitt = false
			continue
		}
		if len(dateien) == 0 {
			continue
		}
		// --- und +++ sind nur vor dem ersten Abschnitt Kopfzeilen, danach Inhalt.
		if strings.HasPrefix(z, "@@") {
			imAbschnitt = true
		} else if !imAbschnitt {
			d := &dateien[len(dateien)-1]
			if n, ok := strings.CutPrefix(z, "+++ "); ok {
				if n = entquotet(strings.TrimSuffix(n, "\t")); n != "/dev/null" {
					d.Name = strings.TrimPrefix(n, "b/")
				}
				continue
			}
			for _, vor := range []string{"rename to ", "copy to "} {
				if n, ok := strings.CutPrefix(z, vor); ok {
					d.Name = entquotet(n)
				}
			}
			if strings.HasPrefix(z, "index ") || strings.HasPrefix(z, "--- ") {
				continue
			}
		}
		akt := &dateien[len(dateien)-1]
		k, nr := "", 0
		switch {
		case strings.HasPrefix(z, "@@"):
			k = "abschnitt"
			if m := abschnittKopf.FindStringSubmatch(z); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					neu = n
				}
			}
		case strings.HasPrefix(z, "+"):
			k, nr = "plus", neu
			akt.Plus++
			neu++
		case strings.HasPrefix(z, "-"):
			k = "minus"
			akt.Minus++
		case imAbschnitt && strings.HasPrefix(z, " "):
			nr = neu
			neu++
		}
		gesamt++
		if len(akt.Zeilen) < 2000 && gesamt < 20000 {
			akt.Zeilen = append(akt.Zeilen, DiffZeile{Klasse: k, Text: z, Neu: nr})
		} else {
			akt.Gekuerzt = true
		}
	}
	return dateien
}

// diffName gilt nur, solange keine +++- oder rename-Zeile den Namen nennt. Dann
// sind beide Hälften der Kopfzeile derselbe Name, getrennt genau in der Mitte.
func diffName(z string) string {
	s := strings.TrimPrefix(z, "diff --git ")
	if m := len(s) / 2; len(s)%2 == 1 && s[m] == ' ' {
		a, b := entquotet(s[:m]), entquotet(s[m+1:])
		if n, ok := strings.CutPrefix(b, "b/"); ok && a == "a/"+n {
			return n
		}
	}
	return s
}

// entquotet löst, was git bei Namen mit Steuerzeichen in Anführung setzt.
func entquotet(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
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
	// git-http-backend nimmt den letzten gleichnamigen Parameter, Go den ersten:
	// geprüft und ausgeführt wäre sonst nicht dasselbe.
	dienst, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(dienst["service"]) > 1 || strings.Contains(r.URL.RawQuery, ";") {
		verweigern(http.StatusBadRequest, "Anfrage mehrdeutig")
		return
	}
	switch dienst.Get("service") {
	case "", "git-upload-pack", "git-receive-pack":
	default:
		verweigern(http.StatusBadRequest, "Dienst unbekannt")
		return
	}
	// Nur den geprüften Wert weitergeben, nicht die Rohform.
	r.URL.RawQuery = ""
	if d := dienst.Get("service"); d != "" {
		r.URL.RawQuery = "service=" + d
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
	h := &cgi.Handler{Path: backend, Dir: a.repos}
	konfig := [][2]string{{"receive.hideRefs", "refs/replace"}}
	h.Env = []string{
		"GIT_PROJECT_ROOT=" + a.repos,
		"GIT_HTTP_EXPORT_ALL=1",
		"GIT_HTTP_MAX_REQUEST_BUFFER=100M",
		"REMOTE_USER=" + wer.Name,
		// refs/replace täuschte jedem Leser andere Inhalte vor.
		"GIT_NO_REPLACE_OBJECTS=1",
		"HOME=" + os.Getenv("HOME"),
	}
	if len(r.TransferEncoding) > 0 {
		grenze := int64(100 << 20)
		if schreiben {
			grenze = 2 << 30
		}
		f, n, err := aufPlatte(r.Body, grenze)
		if errors.Is(err, errZuGross) {
			verweigern(http.StatusRequestEntityTooLarge, "zu groß")
			return
		} else if err != nil {
			log.Printf("git %s: %v", repo.Voll(), err)
			verweigern(http.StatusBadRequest, "Anfrage unvollständig")
			return
		}
		defer f.Close()
		r.Body, r.ContentLength, r.TransferEncoding = f, n, nil
	}
	if schreiben && rest == "/git-receive-pack" {
		ordner, err := hookOrdner()
		if err != nil {
			log.Printf("push %s: %v", repo.Voll(), err)
			verweigern(http.StatusInternalServerError, "Push geht gerade nicht")
			return
		}
		meldung, err := os.CreateTemp("", "forgr-push-")
		if err != nil {
			log.Printf("push %s: %v", repo.Voll(), err)
			verweigern(http.StatusInternalServerError, "Push geht gerade nicht")
			return
		}
		konfig = append(konfig, [2]string{"core.hooksPath", ordner})
		h.Env = append(h.Env, "FORGR_PUSH="+meldung.Name())
		defer func() {
			defer os.Remove(meldung.Name())
			defer meldung.Close()
			ctx, ende := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
			defer ende()
			if _, err := a.db.Exec(ctx, `UPDATE repos SET geaendert=now() WHERE id::text=$1`, repo.ID); err != nil {
				log.Printf("push %s: %v", repo.Voll(), err)
			}
			if l, err := pushRefs(meldung); err != nil {
				log.Printf("push %s: %v", repo.Voll(), err)
			} else if len(l) > 0 {
				a.melden(repo, "push", wer.Name, map[string]any{"refs": l})
			}
		}()
	}
	h.Env = append(h.Env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(konfig)))
	for i, k := range konfig {
		n := strconv.Itoa(i)
		h.Env = append(h.Env, "GIT_CONFIG_KEY_"+n+"="+k[0], "GIT_CONFIG_VALUE_"+n+"="+k[1])
	}
	h.ServeHTTP(w, r)
}
