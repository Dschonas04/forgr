# Forgr

Eigenes Git-Hosting: Repositories, Merge-Requests, Issues und CI-Anbindung, auf dem eigenen Server.

**Stand: Grundgerüst.** Der Server startet und zeigt eine Startseite, die
Funktionen unten sind geplant.

## Geplant

* **Repositories über HTTPS und SSH** (Smart HTTP und eigener SSH-Server in Go)
* **Weboberfläche**: Dateien, Commits, Diffs, Blame, Zweige, Tags, Releases
* **Merge-Requests** mit Review, Kommentaren an Zeilen und Zusammenführen im Browser
* **Issues** mit Labels und Meilensteinen
* **Gruppen und Teams**, Rechte je Repository (lesen, schreiben, verwalten)
* **Webhooks** und Anbindung von CI-Runnern, Status an Commits
* **Spiegel** von und nach GitHub
* **Anmeldung über Keycloak**, SSH-Schlüssel und Zugriffstoken je Konto
* Später: Git LFS, Container-Registry

## Schritte

1. Repositories anlegen, klonen und pushen über Smart HTTP
2. Dateibrowser, Commits, Diffs
3. Konten, Keycloak, Rechte
4. SSH-Zugang mit eigenen Schlüsseln
5. Merge-Requests und Issues
6. Webhooks, CI-Status, Spiegel

## Starten

```bash
cp .env.beispiel .env            # Kennwort für die Datenbank eintragen
docker compose up -d --build
```

Danach unter `http://<host>:8380`. Im Homelab kommt TLS über Caddy mit
der Homelab-CA davor, der Dienst selbst lauscht dann auf `127.0.0.1:28380`.

## Entwickeln

```bash
go run .
go test ./...
```

Go 1.27.4, Datenbank PostgreSQL. Das Schema steht in `schema.sql`.

## Lizenz

MIT, siehe [LICENSE](LICENSE).
