-- Datenbankschema von Forgr (Entwurf).
CREATE TABLE IF NOT EXISTS konten (
	id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	name      text NOT NULL UNIQUE,              -- in Adressen: /<name>/<repo>
	anzeige   text NOT NULL DEFAULT '',
	art       text NOT NULL DEFAULT 'person',    -- person | gruppe
	erstellt  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repos (
	id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	besitzer    uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	name        text NOT NULL,
	beschreibung text NOT NULL DEFAULT '',
	privat      boolean NOT NULL DEFAULT true,
	standardzweig text NOT NULL DEFAULT 'main',
	erstellt    timestamptz NOT NULL DEFAULT now(),
	UNIQUE (besitzer, name)
);

CREATE TABLE IF NOT EXISTS rechte (
	repo   uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	konto  uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	recht  text NOT NULL DEFAULT 'lesen',        -- lesen | schreiben | verwalten
	PRIMARY KEY (repo, konto)
);

CREATE TABLE IF NOT EXISTS ssh_schluessel (
	id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	konto       uuid NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
	name        text NOT NULL,
	fingerabdruck text NOT NULL UNIQUE,
	schluessel  text NOT NULL,
	erstellt    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS merge_requests (
	id        bigserial PRIMARY KEY,
	repo      uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	quelle    text NOT NULL,
	ziel      text NOT NULL,
	titel     text NOT NULL,
	status    text NOT NULL DEFAULT 'offen',     -- offen | zusammengefuehrt | geschlossen
	verfasser uuid REFERENCES konten(id),
	erstellt  timestamptz NOT NULL DEFAULT now()
);
