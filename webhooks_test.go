package main

import "testing"

// Go macht aus İ ein i, der Transport einen anderen Host.
func TestWebhookHostNurASCII(t *testing.T) {
	h := neueHooks("https://intern.example", "")
	for _, z := range []string{"https://İntern.example/h", "https://%C4%B0ntern.example/h"} {
		if h.erlaubt(z) {
			t.Errorf("an der Liste vorbei: %q", z)
		}
	}
	if !h.erlaubt("https://INTERN.example/h") {
		t.Error("erlaubtes Ziel abgelehnt")
	}
}
