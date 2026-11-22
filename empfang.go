package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Was ein Push geändert hat, sagt git im post-receive. Ein Vergleich davor und
// danach schriebe ihm auch zu, was jemand anderes inzwischen geändert hat.
var hookOrdner = sync.OnceValues(func() (string, error) {
	d, err := os.MkdirTemp("", "forgr-hooks-")
	if err != nil {
		return "", err
	}
	const skript = "#!/bin/sh\ncat >> \"$FORGR_PUSH\"\n"
	if err := os.WriteFile(filepath.Join(d, "post-receive"), []byte(skript), 0o700); err != nil {
		return "", err
	}
	return d, nil
})

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
