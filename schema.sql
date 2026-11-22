-- Datenbankschema von Forgr. Idempotent: wird bei jedem Start eingespielt.
CREATE TABLE IF NOT EXISTS konten (
	id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	kennung   text NOT NULL UNIQUE,              -- Keycloak-sub oder lokaler Name
	name      text NOT NULL UNIQUE,              -- in Adressen: /<name>/<repo>
	anzeige   text NOT NULL DEFAULT '',
	email     text NOT NULL DEFAULT '',
	erstellt  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repos (
	id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	besitzer      uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	name          text NOT NULL,
	beschreibung  text NOT NULL DEFAULT '',
	privat        boolean NOT NULL DEFAULT true,
	erstellt      timestamptz NOT NULL DEFAULT now(),
	geaendert     timestamptz NOT NULL DEFAULT now(),
	UNIQUE (besitzer, name)
);

CREATE TABLE IF NOT EXISTS rechte (
	repo   uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	konto  uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	recht  text NOT NULL DEFAULT 'lesen',        -- lesen | schreiben
	PRIMARY KEY (repo, konto)
);

-- Zugriffstoken für git über HTTPS. Gespeichert wird nur der Hash.
CREATE TABLE IF NOT EXISTS tokens (
	id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	konto     uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	name      text NOT NULL,
	hash      text NOT NULL UNIQUE,
	anfang    text NOT NULL,
	erstellt  timestamptz NOT NULL DEFAULT now(),
	zuletzt   timestamptz
);

CREATE TABLE IF NOT EXISTS issues (
	id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	repo      uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	nummer    int NOT NULL,
	titel     text NOT NULL,
	offen     boolean NOT NULL DEFAULT true,
	verfasser uuid REFERENCES konten(id) ON DELETE SET NULL,
	erstellt  timestamptz NOT NULL DEFAULT now(),
	geaendert timestamptz NOT NULL DEFAULT now(),
	UNIQUE (repo, nummer)
);

CREATE TABLE IF NOT EXISTS kommentare (
	id        bigserial PRIMARY KEY,
	issue     uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
	verfasser uuid REFERENCES konten(id) ON DELETE SET NULL,
	text      text NOT NULL,
	erstellt  timestamptz NOT NULL DEFAULT now()
);

-- Wer die App eingerichtet hat und verwaltet. Dazu kommt FORGR_ADMIN.
CREATE TABLE IF NOT EXISTS admins (
	kennung text PRIMARY KEY,
	seit    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE issues ADD COLUMN IF NOT EXISTS labels text[] NOT NULL DEFAULT '{}';
ALTER TABLE issues ADD COLUMN IF NOT EXISTS meilenstein text NOT NULL DEFAULT '';

-- Merge-Requests zwischen Zweigen eines Repositorys.
CREATE TABLE IF NOT EXISTS mrs (
	id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	repo       uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	nummer     int NOT NULL,
	titel      text NOT NULL,
	text       text NOT NULL DEFAULT '',
	quelle     text NOT NULL,
	ziel       text NOT NULL,
	zustand    text NOT NULL DEFAULT 'offen',   -- offen | zusammengefuehrt | geschlossen
	verfasser  uuid REFERENCES konten(id) ON DELETE SET NULL,
	quelle_sha text NOT NULL DEFAULT '',        -- beim Zusammenführen festgehalten
	ziel_sha   text NOT NULL DEFAULT '',
	merge_sha  text NOT NULL DEFAULT '',
	erstellt   timestamptz NOT NULL DEFAULT now(),
	geaendert  timestamptz NOT NULL DEFAULT now(),
	UNIQUE (repo, nummer)
);

CREATE TABLE IF NOT EXISTS mr_beitraege (
	id        bigserial PRIMARY KEY,
	mr        uuid NOT NULL REFERENCES mrs(id) ON DELETE CASCADE,
	verfasser uuid REFERENCES konten(id) ON DELETE SET NULL,
	art       text NOT NULL DEFAULT 'kommentar', -- kommentar | zustimmung | aenderung
	text      text NOT NULL DEFAULT '',
	datei     text NOT NULL DEFAULT '',
	zeile     int NOT NULL DEFAULT 0,
	sha       text NOT NULL DEFAULT '',          -- Stand der Quelle, auf den er sich bezieht
	erstellt  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS releases (
	repo      uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	tag       text NOT NULL,
	titel     text NOT NULL,
	text      text NOT NULL DEFAULT '',
	erstellt  timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (repo, tag)
);

-- Gruppen: wer eine anlegt, verwaltet sie und gibt ihr Rechte an eigenen Repos.
CREATE TABLE IF NOT EXISTS teams (
	id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	besitzer  uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	name      text NOT NULL,
	UNIQUE (besitzer, name)
);

CREATE TABLE IF NOT EXISTS team_mitglieder (
	team   uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
	konto  uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	PRIMARY KEY (team, konto)
);

CREATE TABLE IF NOT EXISTS team_rechte (
	repo   uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	team   uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
	recht  text NOT NULL DEFAULT 'lesen',        -- lesen | schreiben | verwalten
	PRIMARY KEY (repo, team)
);

-- Ziele müssen in FORGR_WEBHOOK_ZIELE erlaubt sein.
CREATE TABLE IF NOT EXISTS webhooks (
	id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	repo     uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	adresse  text NOT NULL,
	stand    text NOT NULL DEFAULT '',
	zuletzt  timestamptz
);

-- Was CI an einen Commit meldet.
CREATE TABLE IF NOT EXISTS commit_status (
	repo     uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	sha      text NOT NULL,
	name     text NOT NULL,
	zustand  text NOT NULL,                      -- pending | success | failure | error
	adresse  text NOT NULL DEFAULT '',
	text     text NOT NULL DEFAULT '',
	zeit     timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (repo, sha, name)
);
