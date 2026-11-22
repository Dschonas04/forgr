// Forgr: eigenes Git-Hosting. Repositories über HTTPS (Smart HTTP mit
// Zugriffstoken), Weboberfläche mit Dateien, Zweigen, Commits und Diffs,
// README-Anzeige, Issues, Mitglieder mit Rechten, Import von anderen Servern.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	admins []string // Vorgabe des Betreibers
	db     *pgxpool.Pool
	sso    *SSO
	v      Vorlagen
	repos  string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &App{
		db:    datenbank(ctx, umgebung("FORGR_DB", "postgres://forgr:forgr@localhost:5432/forgr?sslmode=disable")),
		sso:   NeuesSSO("Forgr", umgebung("FORGR_SSO_DATEI", "./einstellungen/sso.json"), "FORGR_SSO_AUS"),
		v:     ladeVorlagen(),
		repos: umgebung("FORGR_REPOS", "./repos"),
	}
	a.admins = adminListe(umgebung("FORGR_ADMIN", ""))
	a.sso.ErstesKonto = a.ersterAdmin
	a.sso.Frei = []string{"/statisch/", "/healthz"}
	// git über HTTPS meldet sich mit Zugriffstoken an, nicht mit Sitzung.
	a.sso.Durchlass = func(r *http.Request) bool { return gitPfad.MatchString(r.URL.Path) }
	if err := os.MkdirAll(a.repos, 0o750); err != nil {
		log.Fatalf("Repos: %v", err)
	}

	adresse := ":" + umgebung("FORGR_PORT", "8380")
	srv := &http.Server{Addr: adresse, Handler: a.wege(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("Forgr %s lauscht auf %s", version, adresse)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	ende, abbruch := context.WithTimeout(context.Background(), 10*time.Second)
	defer abbruch()
	srv.Shutdown(ende)
}

// gitPfad: /<besitzer>/<repo>.git/...
var gitPfad = regexp.MustCompile(`^/([a-z0-9][a-z0-9._-]*)/([A-Za-z0-9][A-Za-z0-9._-]*)\.git(/.*)?$`)

func (a *App) wege() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", gesund)
	m.HandleFunc("GET /favicon.svg", logo)
	m.HandleFunc("GET /favicon.ico", logo)
	m.Handle("GET /statisch/", statisch())
	a.sso.Wege(m, a.angemeldet)

	m.HandleFunc("GET /{$}", a.start)
	m.HandleFunc("GET /neu", a.neuFormular)
	m.HandleFunc("POST /neu", a.neuAnlegen)
	m.HandleFunc("GET /tokens", a.tokens)
	m.HandleFunc("POST /tokens", a.tokenAnlegen)
	m.HandleFunc("POST /tokens/{id}/loeschen", a.tokenLoeschen)

	// Repository-Seiten in einem eigenen Router: /{b}/{r} würde sonst mit
	// /statisch/ und /sso/ kollidieren.
	rm := http.NewServeMux()
	rm.HandleFunc("GET /{b}/{r}", a.repoStart)
	rm.HandleFunc("GET /{b}/{r}/baum/{ref}/{pfad...}", a.baum)
	rm.HandleFunc("GET /{b}/{r}/datei/{ref}/{pfad...}", a.datei)
	rm.HandleFunc("GET /{b}/{r}/roh/{ref}/{pfad...}", a.roh)
	rm.HandleFunc("GET /{b}/{r}/commits/{ref}", a.commits)
	rm.HandleFunc("GET /{b}/{r}/commit/{sha}", a.commit)
	rm.HandleFunc("GET /{b}/{r}/einstellungen", a.einstellungen)
	rm.HandleFunc("POST /{b}/{r}/einstellungen", a.einstellungenSpeichern)
	rm.HandleFunc("POST /{b}/{r}/mitglied", a.mitgliedSetzen)
	rm.HandleFunc("POST /{b}/{r}/loeschen", a.repoLoeschen)
	rm.HandleFunc("GET /{b}/{r}/issues", a.issues)
	rm.HandleFunc("POST /{b}/{r}/issues", a.issueAnlegen)
	rm.HandleFunc("GET /{b}/{r}/issues/{n}", a.issue)
	rm.HandleFunc("POST /{b}/{r}/issues/{n}", a.issueKommentar)

	m.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		erstes := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
		switch erstes {
		case "statisch", "sso", "api", "neu", "tokens", "healthz":
			http.NotFound(w, r)
			return
		}
		rm.ServeHTTP(w, r)
	}))
	app := nurAngemeldet(a.sso, m)
	return sicherheitskopf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := gitPfad.FindStringSubmatch(r.URL.Path); t != nil {
			a.git(w, r, t[1], t[2], t[3])
			return
		}
		app.ServeHTTP(w, r)
	}))
}

func (a *App) angemeldet(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Anmeldung und Konten verwalten nur Admins; vor der Einrichtung, wer
		// den Code aus dem Log hat.
		if !a.sso.Geschuetzt() && a.sso.EinrichtungErlaubt(w, r) {
			h(w, r)
			return
		}
		if _, ok := a.sso.Wer(r); !ok {
			http.Error(w, "Bitte anmelden", http.StatusUnauthorized)
			return
		}
		if !a.istAdmin(r) {
			http.Error(w, "Nur für Admins", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (a *App) daten(r *http.Request, titel, nav string, d any) Daten {
	n, _ := a.sso.Wer(r)
	return Daten{App: "Forgr", Titel: titel, Nav: nav, Nutzer: n, Admin: a.istAdmin(r), D: d}
}

func basis(r *http.Request) string {
	schema := "http"
	if ssoSicher(r) {
		schema = "https"
	}
	return schema + "://" + r.Host
}

var nameMuster = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func gueltigerName(n string) bool {
	return nameMuster.MatchString(n) && !strings.HasSuffix(n, ".git") && !strings.Contains(n, "..") &&
		n != "neu" && n != "tokens" && n != "statisch" && n != "sso" && n != "api"
}
