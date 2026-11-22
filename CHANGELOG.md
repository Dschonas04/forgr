# Änderungen

## Unveröffentlicht

- Sicherheit: Zweige, Tags und Commits werden eindeutig aufgelöst, ein Tag kann keinen Zweig und keinen Commit überdecken.
- Merge-Requests mit Review, Zeilenkommentaren und Zusammenführen, Blame, Releases, Labels und Meilensteine.
- Sicherheit: mehrdeutige service-Parameter abgelehnt, git erbt die Umgebung der App nicht mehr.
- Zweige mit Schrägstrich, vollständige Diffs, richtiger Text zur Sichtbarkeit.
- Sicherheit: Die SSO-Einstellung hat einen Leser für Prüfung und Speichern und lehnt Unlesbares ab.
- Doppelte id im Seitengerüst behoben.
- Sicherheit: Die Sperre der Verwaltung wartet weder auf den Browser noch auf den Anbieter und hat eine Zeitgrenze.
- Sicherheit: Änderungen an Konten, Rollen und Anmeldung laufen nacheinander, Prüfung und Schreiben unter einer Sperre.
- Sicherheit: Mit einem Konto enden seine Rechte, Token und Repobindung; geprüft wird erst, wenn die Anfrage vollständig da ist.
- Sicherheit: Admin wird man nur mit dem Einrichtungscode oder über `FORGR_ADMIN`, nicht durch die erste Anmeldung. Kennungen von eigenen Konten und SSO sind getrennt.
- Oberfläche überarbeitet: Kopfzeile, Tabellen und Felder am Handy, Anmeldeseite in den Farben der App.
- Logo auf der Anmeldeseite wird wieder ausgeliefert, der Hinweis zur Ersteinrichtung stimmt.
- Sicherheit: Anmeldung, Konten und Verwaltung nur noch für Admins.
- git über HTTPS mit git-http-backend: klonen, holen, pushen; Anmeldung mit Kontoname und Zugriffstoken (nur der Hash gespeichert); öffentliche Repositories lassen sich ohne Anmeldung klonen.
- Weboberfläche: Repositories anlegen oder von einer https-Adresse importieren, Dateibaum je Zweig, README in Markdown, Dateiansicht mit Zeilennummern, Rohdateien (nie als HTML), Commits mit Diff.
- Issues mit Kommentaren in Markdown, öffnen und schließen.
- Einstellungen: Beschreibung, Sichtbarkeit, Mitglieder mit Lese- oder Schreibrecht, Löschen mit Bestätigung durch den Namen.
- Refs und Pfade werden geprüft und mit --end-of-options an git gegeben.
- Grundgerüst: Server mit Startseite, /healthz und /api/version, Schemaentwurf, Docker, Prüfung in GitHub Actions.
