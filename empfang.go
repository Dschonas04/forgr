package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Was ein Push geändert hat, sagt git im post-receive. Ein Vergleich davor und
// danach schriebe ihm auch zu, was jemand anderes inzwischen geändert hat.
const hookSkript = "#!/bin/sh\ncat >> \"$FORGR_PUSH\"\n"

var hookSperre sync.Mutex

// hookOrdner liegt bei den Repos, nicht in /tmp: was git ausführt, darf kein
// anderer anlegen können. Geprüft wird vor jedem Push. Über HTTP ist er nicht
// erreichbar, Namen dort beginnen nie mit einem Punkt.
func hookOrdner(repos string) (string, error) {
	hookSperre.Lock()
	defer hookSperre.Unlock()
	d, err := filepath.Abs(filepath.Join(repos, ".hooks"))
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	st, err := os.Lstat(d)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("hook-Ordner ist kein Ordner")
	}
	if st.Mode().Perm() != 0o700 {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	ziel := filepath.Join(d, "post-receive")
	if st, err := os.Lstat(ziel); err == nil && st.Mode().IsRegular() && st.Mode().Perm() == 0o700 {
		if b, err := os.ReadFile(ziel); err == nil && string(b) == hookSkript {
			return d, nil
		}
	}
	neu, err := os.CreateTemp(d, ".neu-")
	if err != nil {
		return "", err
	}
	defer os.Remove(neu.Name())
	_, err = neu.WriteString(hookSkript)
	if e := neu.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Chmod(neu.Name(), 0o700)
	}
	if err == nil {
		err = os.Rename(neu.Name(), ziel)
	}
	if err != nil {
		return "", err
	}
	return d, nil
}

// pushRefs liest die Zeilen "alt neu ref" des Hooks.
func pushRefs(datei *os.File) ([]map[string]string, error) {
	// Der Hook hat über den Namen geschrieben, unser Zeiger steht am Anfang.
	b, err := io.ReadAll(io.LimitReader(datei, gitMax))
	if err != nil {
		return nil, err
	}
	var l []map[string]string
	for _, z := range strings.Split(string(b), "\n") {
		if f := strings.Fields(z); len(f) == 3 {
			l = append(l, refAenderung(f[2], f[0], f[1]))
		}
	}
	return l, nil
}

// fetchRefs liest git fetch --porcelain: "<zeichen> alt neu ref".
func fetchRefs(aus []byte) []map[string]string {
	var l []map[string]string
	for _, z := range strings.Split(string(aus), "\n") {
		if len(z) < 2 || z[0] == '!' || z[0] == '=' {
			continue
		}
		if f := strings.Fields(z[1:]); len(f) == 3 {
			l = append(l, refAenderung(f[2], f[0], f[1]))
		}
	}
	return l
}

func refAenderung(ref, vorher, nachher string) map[string]string {
	ohneNull := func(sha string) string {
		if strings.Trim(sha, "0") == "" {
			return ""
		}
		return sha
	}
	return map[string]string{"ref": ref, "vorher": ohneNull(vorher), "nachher": ohneNull(nachher)}
}

var errZuGross = errors.New("zu groß")

// aufPlatte legt einen Inhalt ohne Länge ab: git schickt ab 1 MB stückweise,
// CGI braucht die Länge vorher.
func aufPlatte(r io.Reader, max int64) (*os.File, int64, error) {
	f, err := os.CreateTemp("", "forgr-eingang-")
	if err != nil {
		return nil, 0, err
	}
	// Schon entfernt, solange er offen ist: es bleibt nie etwas liegen.
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if err == nil && n > max {
		err = errZuGross
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, n, nil
}
