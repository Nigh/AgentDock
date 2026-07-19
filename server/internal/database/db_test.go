package database

import (
	"path/filepath"
	"testing"
)

// The legacy single per-user token (users.node_token_hash) must migrate
// into node_tokens exactly once on open.
func TestLegacyTokenMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := db.CreateUser("old", "x", "admin", "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET node_token_hash = 'legacyhash' WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = Open(path) // migration runs here
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tok, err := db.GetNodeTokenByHash("legacyhash")
	if err != nil || tok == nil {
		t.Fatalf("legacy token not migrated: %v %v", tok, err)
	}
	if tok.UserID != uid || tok.Name != "default" {
		t.Fatalf("migrated token = %+v", tok)
	}
	tokens, _ := db.ListNodeTokens(uid)
	if len(tokens) != 1 {
		t.Fatalf("tokens after reopen = %d, want 1", len(tokens))
	}

	// reopening again must not duplicate it
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens, _ = db.ListNodeTokens(uid)
	if len(tokens) != 1 {
		t.Fatalf("tokens after second reopen = %d, want 1", len(tokens))
	}
}
