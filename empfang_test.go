package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHookLaesstSichNichtUnterschieben(t *testing.T) {
	repos := t.TempDir()
	d, err := hookOrdner(repos)
	if err != nil || filepath.Dir(d) != repos {
		t.Fatalf("liegt nicht bei den Repos: %s %v", d, err)
	}
	ziel := filepath.Join(d, "post-receive")
	fremd := filepath.Join(t.TempDir(), "fremd")
	for _, text := range []string{"#!/bin/sh\necho fremd\n", hookSkript} {
		if err := os.Remove(ziel); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fremd, []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(fremd, ziel); err != nil {
			t.Fatal(err)
		}
		if _, err := hookOrdner(repos); err != nil {
			t.Fatal(err)
		}
		st, err := os.Lstat(ziel)
		if err != nil || !st.Mode().IsRegular() {
			t.Fatal("untergeschobener Hook bleibt")
		}
		if b, err := os.ReadFile(ziel); err != nil || string(b) != hookSkript {
			t.Fatal("falscher Inhalt")
		}
	}
}
