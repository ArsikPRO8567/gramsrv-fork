package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/session"
)

// ---------------------------------------------------------------------------
// Test helpers: build a minimal valid SQLite database with the Telethon
// session schema (version + sessions tables) without any SQLite dependency.
// ---------------------------------------------------------------------------

func encodeSQLiteVarint(v uint64) []byte {
	if v >= 1<<56 {
		buf := make([]byte, 9)
		buf[8] = byte(v)
		v >>= 8
		for i := 7; i >= 0; i-- {
			buf[i] = byte(v&0x7f) | 0x80
			v >>= 7
		}
		return buf
	}
	var groups []byte
	for {
		groups = append(groups, byte(v&0x7f))
		v >>= 7
		if v == 0 {
			break
		}
	}
	out := make([]byte, len(groups))
	for i, g := range groups {
		out[len(groups)-1-i] = g
	}
	for i := 0; i < len(out)-1; i++ {
		out[i] |= 0x80
	}
	return out
}

func encodeSQLiteIntBytes(value int64, size int) []byte {
	out := make([]byte, size)
	for i := 0; i < size; i++ {
		out[size-1-i] = byte(value >> (8 * i))
	}
	return out
}

func sqliteRecordBytes(values []any) []byte {
	var header []byte
	var body []byte
	appendValue := func(serial uint64, data []byte) {
		header = append(header, encodeSQLiteVarint(serial)...)
		body = append(body, data...)
	}
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			appendValue(0, nil)
		case int64:
			switch {
			case v == 0:
				appendValue(8, nil)
			case v == 1:
				appendValue(9, nil)
			case v >= -128 && v <= 127:
				appendValue(1, encodeSQLiteIntBytes(v, 1))
			case v >= -32768 && v <= 32767:
				appendValue(2, encodeSQLiteIntBytes(v, 2))
			case v >= -8388608 && v <= 8388607:
				appendValue(3, encodeSQLiteIntBytes(v, 3))
			case v >= -2147483648 && v <= 2147483647:
				appendValue(4, encodeSQLiteIntBytes(v, 4))
			case v >= -140737488355328 && v <= 140737488355327:
				appendValue(5, encodeSQLiteIntBytes(v, 6))
			default:
				appendValue(6, encodeSQLiteIntBytes(v, 8))
			}
		case string:
			appendValue(uint64(len(v))*2+13, []byte(v))
		case []byte:
			appendValue(uint64(len(v))*2+12, v)
		default:
			panic("unsupported record value")
		}
	}
	headerLen := len(header) + 1
	if headerLen >= 128 {
		panic("test records are small enough for a single-byte header length")
	}
	out := []byte{byte(headerLen)}
	out = append(out, header...)
	out = append(out, body...)
	return out
}

func sqliteCell(rowid int64, record []byte) []byte {
	out := encodeSQLiteVarint(uint64(len(record)))
	out = append(out, encodeSQLiteVarint(uint64(rowid))...)
	return append(out, record...)
}

func sqliteLeafPage(pageNumber int, cells [][]byte) []byte {
	const pageSize = 4096
	page := make([]byte, pageSize)
	hdr := 0
	if pageNumber == 1 {
		hdr = 100
	}
	page[hdr] = 0x0D
	binary.BigEndian.PutUint16(page[hdr+3:hdr+5], uint16(len(cells)))
	pointers := hdr + 8
	offset := pointers + 2*len(cells)
	for i, cell := range cells {
		copy(page[offset:], cell)
		binary.BigEndian.PutUint16(page[pointers+2*i:pointers+2*i+2], uint16(offset))
		offset += len(cell)
	}
	binary.BigEndian.PutUint16(page[hdr+5:hdr+7], uint16(offset&0xffff))
	return page
}

func buildTelethonSessionDB(t *testing.T, authKeyColumn any) []byte {
	t.Helper()
	const pageSize = 4096
	masterRecord := sqliteRecordBytes([]any{
		"table",
		"sessions",
		"sessions",
		int64(2),
		"CREATE TABLE sessions (dc_id INTEGER PRIMARY KEY, server_address TEXT, port INTEGER, auth_key BLOB, takeout_id INTEGER)",
	})
	sessionsRecord := sqliteRecordBytes([]any{
		// dc_id is an INTEGER PRIMARY KEY (rowid alias): real SQLite stores
		// NULL in the record and keeps the value in the rowid.
		nil,
		"149.154.167.40",
		int64(443),
		authKeyColumn,
		nil,
	})
	db := append(sqliteLeafPage(1, [][]byte{sqliteCell(1, masterRecord)}), sqliteLeafPage(2, [][]byte{sqliteCell(2, sessionsRecord)})...)
	copy(db, sqliteMagic)
	binary.BigEndian.PutUint16(db[16:18], pageSize)
	db[20] = 0
	binary.BigEndian.PutUint32(db[56:60], 1)
	return db
}

func testAuthKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 256)
	for i := range key {
		key[i] = byte(i*7 + 3)
	}
	return key
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

func TestDetectSessionFormat(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "json.session")
	if err := os.WriteFile(jsonPath, []byte(`{"Version":1,"Data":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	format, err := detectSessionFormat(jsonPath)
	if err != nil {
		t.Fatalf("detect JSON session: %v", err)
	}
	if format != sessionFormatJSON {
		t.Fatalf("format = %v, want JSON", format)
	}

	sqlitePath := filepath.Join(dir, "sqlite.session")
	if err := os.WriteFile(sqlitePath, buildTelethonSessionDB(t, testAuthKey(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	format, err = detectSessionFormat(sqlitePath)
	if err != nil {
		t.Fatalf("detect SQLite session: %v", err)
	}
	if format != sessionFormatSQLite {
		t.Fatalf("format = %v, want SQLite", format)
	}

	bogusPath := filepath.Join(dir, "bogus.session")
	if err := os.WriteFile(bogusPath, []byte("totally not a session"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := detectSessionFormat(bogusPath); err == nil {
		t.Fatalf("bogus session must be rejected")
	}
}

func TestConvertedSessionPath(t *testing.T) {
	if got := convertedSessionPath("/tmp/test_userbot.session"); got != "/tmp/test_userbot.td.session" {
		t.Fatalf("convertedSessionPath = %q", got)
	}
	if got := convertedSessionPath("/tmp/test_userbot"); got != "/tmp/test_userbot.td.session" {
		t.Fatalf("convertedSessionPath = %q", got)
	}
}

func TestReadTelethonSession(t *testing.T) {
	key := testAuthKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "test_userbot.session")
	if err := os.WriteFile(path, buildTelethonSessionDB(t, key), 0o600); err != nil {
		t.Fatal(err)
	}
	row, err := readTelethonSession(path)
	if err != nil {
		t.Fatalf("readTelethonSession: %v", err)
	}
	if row.DC != 2 {
		t.Fatalf("DC = %d, want 2", row.DC)
	}
	if row.ServerAddress != "149.154.167.40" {
		t.Fatalf("ServerAddress = %q", row.ServerAddress)
	}
	if row.Port != 443 {
		t.Fatalf("Port = %d, want 443", row.Port)
	}
	if !bytes.Equal(row.AuthKey, key) {
		t.Fatalf("AuthKey mismatch")
	}
}

func TestReadTelethonSessionHexKey(t *testing.T) {
	key := testAuthKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.session")
	if err := os.WriteFile(path, buildTelethonSessionDB(t, hex.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	row, err := readTelethonSession(path)
	if err != nil {
		t.Fatalf("readTelethonSession: %v", err)
	}
	if !bytes.Equal(row.AuthKey, key) {
		t.Fatalf("AuthKey mismatch after hex decode")
	}
}

func TestReadTelethonSessionRejectsWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.session")
	if err := os.WriteFile(path, buildTelethonSessionDB(t, testAuthKey(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", []byte("pending frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTelethonSession(path); err == nil {
		t.Fatalf("non-empty WAL must be rejected")
	}
}

func TestConvertTelethonSession(t *testing.T) {
	key := testAuthKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "test_userbot.session")
	if err := os.WriteFile(path, buildTelethonSessionDB(t, key), 0o600); err != nil {
		t.Fatal(err)
	}

	target, created, err := convertTelethonSession(path)
	if err != nil {
		t.Fatalf("convertTelethonSession: %v", err)
	}
	if !created {
		t.Fatalf("first conversion must report created")
	}
	if target != filepath.Join(dir, "test_userbot.td.session") {
		t.Fatalf("target = %q", target)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("converted file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("converted file permissions = %v, want 0600", info.Mode().Perm())
	}
	// The original Telethon file must stay untouched.
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(original, []byte(sqliteMagic)) {
		t.Fatalf("original session was modified")
	}

	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := (&session.Loader{Storage: &captureSessionStorage{data: raw}}).Load(context.Background())
	if err != nil {
		t.Fatalf("load converted session: %v", err)
	}
	if loaded.DC != 2 {
		t.Fatalf("loaded DC = %d, want 2", loaded.DC)
	}
	if loaded.Addr != "149.154.167.40:443" {
		t.Fatalf("loaded Addr = %q", loaded.Addr)
	}
	if !bytes.Equal(loaded.AuthKey, key) {
		t.Fatalf("loaded AuthKey mismatch")
	}
	var wantKey crypto.Key
	copy(wantKey[:], key)
	if wantID := wantKey.WithID().ID; !bytes.Equal(loaded.AuthKeyID, wantID[:]) {
		t.Fatalf("loaded AuthKeyID mismatch")
	}

	// Re-running must reuse the converted file.
	target2, created2, err := convertTelethonSession(path)
	if err != nil {
		t.Fatalf("second convertTelethonSession: %v", err)
	}
	if created2 {
		t.Fatalf("second conversion must reuse the file")
	}
	if target2 != target {
		t.Fatalf("second target = %q, want %q", target2, target)
	}

	// A corrupted converted file is re-converted on the next run.
	if err := os.WriteFile(target, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	target3, created3, err := convertTelethonSession(path)
	if err != nil {
		t.Fatalf("convert after corruption: %v", err)
	}
	if !created3 {
		t.Fatalf("conversion after corruption must re-create the file")
	}
	if target3 != target {
		t.Fatalf("target after corruption = %q, want %q", target3, target)
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&session.Loader{Storage: &captureSessionStorage{data: raw}}).Load(context.Background()); err != nil {
		t.Fatalf("re-converted session must load: %v", err)
	}
}

func TestSQLiteVarint(t *testing.T) {
	cases := []struct {
		encoded []byte
		value   uint64
	}{
		{[]byte{0x00}, 0},
		{[]byte{0x02}, 2},
		{[]byte{0x7f}, 127},
		{[]byte{0x81, 0x00}, 128},
		{[]byte{0xe8, 0x54}, 13396},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, 0xffffffffffffffff},
	}
	for _, tc := range cases {
		value, size := sqliteVarint(tc.encoded)
		if value != tc.value {
			t.Fatalf("sqliteVarint(% x) = %d, want %d", tc.encoded, value, tc.value)
		}
		if size != len(tc.encoded) {
			t.Fatalf("sqliteVarint(% x) consumed %d bytes, want %d", tc.encoded, size, len(tc.encoded))
		}
	}
}

func TestSQLiteParseRecord(t *testing.T) {
	payload := sqliteRecordBytes([]any{
		int64(2),
		"149.154.167.40",
		int64(443),
		[]byte{1, 2, 3},
		nil,
		int64(0),
		int64(1),
	})
	values, err := sqliteParseRecord(payload)
	if err != nil {
		t.Fatalf("sqliteParseRecord: %v", err)
	}
	if len(values) != 7 {
		t.Fatalf("parsed %d values, want 7", len(values))
	}
	if v, ok := values[0].(int64); !ok || v != 2 {
		t.Fatalf("values[0] = %#v", values[0])
	}
	if v, ok := values[1].(string); !ok || v != "149.154.167.40" {
		t.Fatalf("values[1] = %#v", values[1])
	}
	if v, ok := values[2].(int64); !ok || v != 443 {
		t.Fatalf("values[2] = %#v", values[2])
	}
	if v, ok := values[3].([]byte); !ok || !bytes.Equal(v, []byte{1, 2, 3}) {
		t.Fatalf("values[3] = %#v", values[3])
	}
	if values[4] != nil {
		t.Fatalf("values[4] = %#v, want nil", values[4])
	}
	if v, ok := values[5].(int64); !ok || v != 0 {
		t.Fatalf("values[5] = %#v", values[5])
	}
	if v, ok := values[6].(int64); !ok || v != 1 {
		t.Fatalf("values[6] = %#v", values[6])
	}
}

func TestSQLiteSignedInt(t *testing.T) {
	if got := sqliteSignedInt([]byte{0x02}); got != 2 {
		t.Fatalf("sqliteSignedInt(02) = %d", got)
	}
	if got := sqliteSignedInt([]byte{0xff}); got != -1 {
		t.Fatalf("sqliteSignedInt(ff) = %d", got)
	}
	if got := sqliteSignedInt([]byte{0xfe, 0xdc}); got != -292 {
		t.Fatalf("sqliteSignedInt(fedc) = %d", got)
	}
	if got := sqliteSignedInt([]byte{0x01, 0xbb}); got != 443 {
		t.Fatalf("sqliteSignedInt(01bb) = %d", got)
	}
}
