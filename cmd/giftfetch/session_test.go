package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/session"
)

func loadTestTelethonSession(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "telethon-session-sqlite.bin"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fixtureTelethonKey mirrors the auth key embedded in
// testdata/telethon-session-sqlite.bin (bytes 0..255).
var fixtureTelethonKey = func() []byte {
	key := make([]byte, 256)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}()

func tdSessionJSON(t *testing.T, dc int, addr string, key []byte) []byte {
	t.Helper()
	id := crypto.Key(key).ID()
	encoded, err := json.Marshal(tdSessionEnvelope{
		Version: 1,
		Data: session.Data{
			DC:        dc,
			Addr:      addr,
			AuthKey:   key,
			AuthKeyID: id[:],
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestResolveSessionPathConvertsTelethonSQLite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "test_userbot.session")
	if err := os.WriteFile(source, loadTestTelethonSession(t), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveSessionPath(source)
	if err != nil {
		t.Fatal(err)
	}
	converted := filepath.Join(dir, "test_userbot.td.session")
	if resolved != converted {
		t.Fatalf("resolved = %q, want %q", resolved, converted)
	}
	raw, err := os.ReadFile(converted)
	if err != nil {
		t.Fatal(err)
	}
	var envelope tdSessionEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 1 || envelope.Data.DC != 2 || envelope.Data.Addr != "149.154.167.40:443" {
		t.Fatalf("converted session = version %d dc %d addr %q", envelope.Version, envelope.Data.DC, envelope.Data.Addr)
	}
	wantKeyID := crypto.Key(fixtureTelethonKey).ID()
	if string(envelope.Data.AuthKey) != string(fixtureTelethonKey) {
		t.Fatal("converted auth key mismatch")
	}
	if string(envelope.Data.AuthKeyID) != string(wantKeyID[:]) {
		t.Fatal("converted auth key id mismatch")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("original telethon session must stay untouched: %v", err)
	}
	info, err := os.Stat(converted)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("converted session mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestResolveSessionPathReusesConvertedSession(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "test_userbot.session")
	if err := os.WriteFile(source, loadTestTelethonSession(t), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := tdSessionJSON(t, 3, "127.0.0.1:443", fixtureTelethonKey)
	converted := filepath.Join(dir, "test_userbot.td.session")
	if err := os.WriteFile(converted, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveSessionPath(source)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != converted {
		t.Fatalf("resolved = %q, want %q", resolved, converted)
	}
	raw, err := os.ReadFile(converted)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(sentinel) {
		t.Fatal("existing converted session must be reused without rewriting")
	}
}

func TestResolveSessionPathPassesThroughTDSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.td.session")
	data := tdSessionJSON(t, 2, "149.154.167.40:443", fixtureTelethonKey)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveSessionPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != path {
		t.Fatalf("resolved = %q, want %q", resolved, path)
	}
}

func TestResolveSessionPathRejectsBrokenSQLite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "broken.session")
	raw := append([]byte("SQLite format 3\x00"), make([]byte, 4096)...)
	if err := os.WriteFile(source, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSessionPath(source); err == nil {
		t.Fatal("expected conversion error for a truncated SQLite session")
	}
}

func TestConvertTelethonSessionRejectsNonSQLite(t *testing.T) {
	if _, err := convertTelethonSession([]byte("not a database")); err == nil {
		t.Fatal("expected error for non-SQLite payload")
	}
}

func TestSQLiteCreateTableColumns(t *testing.T) {
	columns, err := sqliteCreateTableColumns("CREATE TABLE sessions (dc_id integer, server_address text, port integer, auth_key blob, takeout_id integer)")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dc_id", "server_address", "port", "auth_key", "takeout_id"}
	if len(columns) != len(want) {
		t.Fatalf("columns = %v", columns)
	}
	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("columns = %v, want %v", columns, want)
		}
	}
	quoted, err := sqliteCreateTableColumns(`CREATE TABLE "sessions" ("dc_id" INTEGER, server_address TEXT, port INTEGER, auth_key BLOB, takeout_id INTEGER, PRIMARY KEY (dc_id))`)
	if err != nil {
		t.Fatal(err)
	}
	if len(quoted) != len(want) || quoted[0] != "dc_id" {
		t.Fatalf("quoted columns = %v", quoted)
	}
}

func TestSQLiteVarint(t *testing.T) {
	if value, used, err := sqliteVarint([]byte{0x40}); err != nil || value != 64 || used != 1 {
		t.Fatalf("sqliteVarint(0x40) = (%d, %d, %v)", value, used, err)
	}
	if value, used, err := sqliteVarint([]byte{0x81, 0x00}); err != nil || value != 128 || used != 2 {
		t.Fatalf("sqliteVarint(0x81 0x00) = (%d, %d, %v)", value, used, err)
	}
	if _, _, err := sqliteVarint([]byte{0x81}); err == nil {
		t.Fatal("expected truncated varint error")
	}
}
