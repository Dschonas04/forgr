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
