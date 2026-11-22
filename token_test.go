package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTokenEndetMitDemKonto(t *testing.T) {
	adresse := os.Getenv("FORGR_TEST_DB")
	if adresse == "" {
		t.Skip("FORGR_TEST_DB ist nicht gesetzt")
	}
	c, err := pgxpool.ParseConfig(adresse)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.ConnConfig.Database, "pruef") {
		t.Fatal("FORGR_TEST_DB muss auf eine Datenbank pruef* zeigen")
	}
	ctx := context.Background()
	a := &App{db: datenbank(ctx, adresse)}
	defer a.db.Close()
	if _, err := a.db.Exec(ctx, `DELETE FROM konten`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := a.db.QueryRow(ctx, `INSERT INTO konten (kennung, name) VALUES ('konto:anna', 'anna') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	const token = "nur-fuer-den-test"
	if _, err := a.db.Exec(ctx, `INSERT INTO tokens (konto, name, hash, anfang) VALUES ($1::uuid, 'test', $2, 'nur')`, id, tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.tokenPruefen(ctx, "anna", token); !ok {
		t.Fatal("Token gilt schon vorher nicht")
	}
	if err := a.adminEntziehen(ctx, "konto:anna"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.tokenPruefen(ctx, "anna", token); ok {
		t.Error("Token überlebt das Konto")
	}
}
