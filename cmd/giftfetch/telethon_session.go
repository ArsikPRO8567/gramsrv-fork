package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/session"
)

// Telethon stores user sessions as SQLite databases (*.session), while this
// tool (and the whole gotd stack) consumes JSON session files. The reader
// below extracts the single authorized session row from a Telethon database
// and convertTelethonSession re-encodes it into the JSON format produced by
// session.Loader, so both session kinds are accepted by giftfetch.
//
// The reader is deliberately minimal: it parses just enough of the SQLite
// file format to walk the b-tree of one table (SQLite File Format,
// https://www.sqlite.org/fileformat2.html), with no new dependencies.

const sqliteMagic = "SQLite format 3\x00"

const (
	sqlitePageInteriorTable = 0x05
	sqlitePageLeafTable     = 0x0D
)

const (
	maxSQLiteFileBytes = 256 << 20
	maxSQLiteTreeDepth = 32
)

type sessionFormat int

const (
	sessionFormatJSON sessionFormat = iota
	sessionFormatSQLite
)

// detectSessionFormat inspects the session file header.
func detectSessionFormat(path string) (sessionFormat, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	prefix := make([]byte, 16)
	read, err := io.ReadFull(file, prefix)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	prefix = prefix[:read]
	if bytes.HasPrefix(prefix, []byte(sqliteMagic)) {
		return sessionFormatSQLite, nil
	}
	trimmed := bytes.TrimLeft(prefix, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return sessionFormatJSON, nil
	}
	return 0, fmt.Errorf("unsupported session file %s: neither a gotd JSON session nor a SQLite (Telethon) session", path)
}

// convertedSessionPath returns the JSON session path derived from a Telethon
// session path: /path/to/test_userbot.session -> /path/to/test_userbot.td.session.
func convertedSessionPath(path string) string {
	if trimmed := strings.TrimSuffix(path, ".session"); trimmed != path {
		return trimmed + ".td.session"
	}
	return path + ".td.session"
}

// convertTelethonSession converts a Telethon SQLite session file into the
// JSON session format used by gotd. The result is written next to the
// original with 0600 permissions and reused on subsequent runs; the original
// Telethon file is never modified. The second return value reports whether a
// new conversion happened (as opposed to reusing the previous result).
func convertTelethonSession(path string) (string, bool, error) {
	target := convertedSessionPath(path)
	if ok, err := isJSONSessionFile(target); err == nil && ok {
		return target, false, nil
	}
	row, err := readTelethonSession(path)
	if err != nil {
		return "", false, err
	}
	var key crypto.Key
	copy(key[:], row.AuthKey)
	id := key.WithID().ID
	data := &session.Data{
		DC:        row.DC,
		Addr:      fmt.Sprintf("%s:%d", row.ServerAddress, row.Port),
		AuthKey:   key[:],
		AuthKeyID: id[:],
	}
	storage := &captureSessionStorage{}
	if err := (&session.Loader{Storage: storage}).Save(context.Background(), data); err != nil {
		return "", false, fmt.Errorf("marshal converted session: %w", err)
	}
	if err := writeSessionFileAtomic(target, storage.data); err != nil {
		return "", false, err
	}
	return target, true, nil
}

func isJSONSessionFile(path string) (bool, error) {
	format, err := detectSessionFormat(path)
	if err != nil {
		return false, err
	}
	return format == sessionFormatJSON, nil
}

// captureSessionStorage captures the bytes produced by session.Loader so the
// converter writes exactly the JSON envelope gotd itself consumes.
type captureSessionStorage struct {
	data []byte
}

func (s *captureSessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	if len(s.data) == 0 {
		return nil, session.ErrNotFound
	}
	return s.data, nil
}

func (s *captureSessionStorage) StoreSession(ctx context.Context, data []byte) error {
	s.data = append([]byte(nil), data...)
	return nil
}

func writeSessionFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".giftfetch-session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

type telethonSessionRow struct {
	DC            int
	ServerAddress string
	Port          int
	AuthKey       []byte
}

// readTelethonSession extracts the authorized session row (DC id, server
// address, port and 256-byte authorization key) from a Telethon SQLite
// session file.
func readTelethonSession(path string) (telethonSessionRow, error) {
	if wal, err := os.Stat(path + "-wal"); err == nil && wal.Size() > 0 {
		return telethonSessionRow{}, fmt.Errorf("%s-wal is not empty: open and close the Telethon session once (or checkpoint the WAL) before converting", path)
	}
	db, err := openSQLite(path)
	if err != nil {
		return telethonSessionRow{}, err
	}
	root, err := db.findTableRoot("sessions")
	if err != nil {
		return telethonSessionRow{}, err
	}
	var row telethonSessionRow
	found := false
	walkErr := db.walkTable(root, func(rowid int64, record []any) error {
		if found || len(record) < 4 {
			return nil
		}
		parsed := parseTelethonSessionRow(rowid, record)
		if parsed == nil {
			return nil
		}
		row, found = *parsed, true
		return nil
	})
	if walkErr != nil {
		return telethonSessionRow{}, walkErr
	}
	if !found {
		return telethonSessionRow{}, fmt.Errorf("sessions table has no usable row (is the session authorized?)")
	}
	return row, nil
}

// parseTelethonSessionRow decodes one row of the Telethon "sessions" table:
// (dc_id INTEGER PRIMARY KEY, server_address TEXT, port INTEGER, auth_key
// BLOB, takeout_id INTEGER). dc_id is a rowid alias, so SQLite stores it as
// NULL inside the record and keeps the actual value in the rowid; the rowid
// is used as the fallback. Rows that do not carry a usable authorization key
// are skipped.
func parseTelethonSessionRow(rowid int64, record []any) *telethonSessionRow {
	dc, ok := sqliteInt(record[0])
	if !ok {
		dc = rowid
	}
	if dc <= 0 || dc > 1000 {
		return nil
	}
	server, ok := record[1].(string)
	if !ok || server == "" {
		return nil
	}
	port, ok := sqliteInt(record[2])
	if !ok || port <= 0 || port > 65535 {
		return nil
	}
	var rawKey string
	switch value := record[3].(type) {
	case []byte:
		rawKey = string(value)
	case string:
		// Very old Telethon releases stored the key as a hex string.
		rawKey = value
	default:
		return nil
	}
	key, err := decodeTelethonAuthKey(rawKey)
	if err != nil {
		return nil
	}
	return &telethonSessionRow{DC: int(dc), ServerAddress: server, Port: int(port), AuthKey: key}
}

func decodeTelethonAuthKey(raw string) ([]byte, error) {
	switch {
	case len(raw) == 256:
		return []byte(raw), nil
	case len(raw) == 512:
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("telethon auth key hex: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("telethon auth key has unexpected length %d", len(raw))
	}
}

// ---------------------------------------------------------------------------
// Minimal read-only SQLite file reader.
// ---------------------------------------------------------------------------

type sqliteDB struct {
	data       []byte
	pageSize   int
	usableSize int
}

func openSQLite(path string) (*sqliteDB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() < 512 {
		return nil, fmt.Errorf("sqlite file %s is too small (%d bytes)", path, info.Size())
	}
	if info.Size() > maxSQLiteFileBytes {
		return nil, fmt.Errorf("sqlite file %s is too large (%d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte(sqliteMagic)) {
		return nil, fmt.Errorf("file %s is not a SQLite database", path)
	}
	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("sqlite page size %d is invalid", pageSize)
	}
	reserved := int(data[20])
	if reserved < 0 || pageSize-reserved < 480 {
		return nil, fmt.Errorf("sqlite reserved page space %d is invalid", reserved)
	}
	encoding := binary.BigEndian.Uint32(data[56:60])
	if encoding != 1 {
		return nil, fmt.Errorf("sqlite text encoding %d is not supported (want UTF-8)", encoding)
	}
	return &sqliteDB{data: data, pageSize: pageSize, usableSize: pageSize - reserved}, nil
}

func (db *sqliteDB) page(number int) ([]byte, error) {
	if number < 1 {
		return nil, fmt.Errorf("sqlite page number %d is invalid", number)
	}
	offset := (number - 1) * db.pageSize
	if offset+db.pageSize > len(db.data) {
		return nil, fmt.Errorf("sqlite page %d is out of range", number)
	}
	return db.data[offset : offset+db.pageSize], nil
}

// findTableRoot locates the b-tree root page of a table by name via the
// sqlite_master catalog on page 1.
func (db *sqliteDB) findTableRoot(name string) (int, error) {
	var root int
	err := db.walkTable(1, func(_ int64, record []any) error {
		if len(record) < 5 {
			return nil
		}
		objectType, _ := record[0].(string)
		objectName, _ := record[1].(string)
		if objectType != "table" || objectName != name {
			return nil
		}
		pageNumber, ok := sqliteInt(record[3])
		if !ok || pageNumber < 1 {
			return nil
		}
		root = int(pageNumber)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if root == 0 {
		return 0, fmt.Errorf("sqlite table %q not found", name)
	}
	return root, nil
}

// walkTable visits every row of a table b-tree in cell order.
func (db *sqliteDB) walkTable(root int, visit func(rowid int64, record []any) error) error {
	var walk func(pageNumber, depth int) error
	walk = func(pageNumber, depth int) error {
		if depth > maxSQLiteTreeDepth {
			return fmt.Errorf("sqlite b-tree is too deep")
		}
		page, err := db.page(pageNumber)
		if err != nil {
			return err
		}
		headerOffset := 0
		if pageNumber == 1 {
			headerOffset = 100
		}
		switch page[headerOffset] {
		case sqlitePageInteriorTable:
			cellCount := int(binary.BigEndian.Uint16(page[headerOffset+3 : headerOffset+5]))
			pointers := headerOffset + 12
			for i := 0; i < cellCount; i++ {
				ptr := int(binary.BigEndian.Uint16(page[pointers+2*i : pointers+2*i+2]))
				if ptr+4 > len(page) {
					return fmt.Errorf("sqlite interior cell pointer %d is out of range", ptr)
				}
				if err := walk(int(binary.BigEndian.Uint32(page[ptr:ptr+4])), depth+1); err != nil {
					return err
				}
			}
			rightmost := int(binary.BigEndian.Uint32(page[headerOffset+8 : headerOffset+12]))
			return walk(rightmost, depth+1)
		case sqlitePageLeafTable:
			cellCount := int(binary.BigEndian.Uint16(page[headerOffset+3 : headerOffset+5]))
			pointers := headerOffset + 8
			for i := 0; i < cellCount; i++ {
				ptr := int(binary.BigEndian.Uint16(page[pointers+2*i : pointers+2*i+2]))
				rowid, record, err := db.readLeafCell(page, ptr)
				if err != nil {
					return err
				}
				if err := visit(rowid, record); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unexpected sqlite page type 0x%02x", page[headerOffset])
		}
	}
	return walk(root, 0)
}

// readLeafCell decodes one table-leaf cell, following the overflow chain when
// the payload does not fit on the page.
func (db *sqliteDB) readLeafCell(page []byte, ptr int) (int64, []any, error) {
	if ptr < 0 || ptr >= len(page) {
		return 0, nil, fmt.Errorf("sqlite cell pointer %d is out of range", ptr)
	}
	buf := page[ptr:]
	payloadLen, n := sqliteVarint(buf)
	rowid, m := sqliteVarint(buf[n:])
	offset := n + m
	length := int(payloadLen)
	if length < 0 || length > len(db.data) {
		return 0, nil, fmt.Errorf("sqlite payload length %d is out of range", length)
	}
	localLimit := db.usableSize - 35
	var payload []byte
	if length <= localLimit {
		if ptr+offset+length > len(page) {
			return 0, nil, fmt.Errorf("sqlite cell payload is out of range")
		}
		payload = page[ptr+offset : ptr+offset+length]
	} else {
		minLocal := (db.usableSize-12)*32/255 - 23
		k := minLocal + int(int64(length-minLocal)%int64(db.usableSize-4))
		local := k
		if k > localLimit {
			local = minLocal
		}
		if ptr+offset+local+4 > len(page) {
			return 0, nil, fmt.Errorf("sqlite cell payload is out of range")
		}
		payload = append([]byte{}, page[ptr+offset:ptr+offset+local]...)
		next := int(binary.BigEndian.Uint32(page[ptr+offset+local : ptr+offset+local+4]))
		remaining := length - local
		for remaining > 0 {
			if next == 0 {
				return 0, nil, fmt.Errorf("sqlite overflow chain is truncated")
			}
			overflow, err := db.page(next)
			if err != nil {
				return 0, nil, err
			}
			next = int(binary.BigEndian.Uint32(overflow[:4]))
			chunk := db.usableSize - 4
			if chunk > remaining {
				chunk = remaining
			}
			payload = append(payload, overflow[4:4+chunk]...)
			remaining -= chunk
		}
	}
	record, err := sqliteParseRecord(payload)
	if err != nil {
		return 0, nil, err
	}
	return int64(rowid), record, nil
}

// sqliteVarint decodes a SQLite variable-length integer (1-9 bytes).
func sqliteVarint(buf []byte) (uint64, int) {
	var value uint64
	for i := 0; i < 8 && i < len(buf); i++ {
		b := buf[i]
		value = value<<7 | uint64(b&0x7f)
		if b&0x80 == 0 {
			return value, i + 1
		}
	}
	if len(buf) >= 9 {
		value = value<<8 | uint64(buf[8])
		return value, 9
	}
	return value, len(buf)
}

// sqliteParseRecord decodes a record (row) payload into Go values: NULL,
// int64, float64, string or []byte.
func sqliteParseRecord(payload []byte) ([]any, error) {
	headerLen, n := sqliteVarint(payload)
	if headerLen == 0 || int(headerLen) > len(payload) {
		return nil, fmt.Errorf("sqlite record header length %d is out of range", headerLen)
	}
	header := payload[n:headerLen]
	body := payload[headerLen:]
	var serialTypes []uint64
	for len(header) > 0 {
		t, m := sqliteVarint(header)
		header = header[m:]
		serialTypes = append(serialTypes, t)
	}
	values := make([]any, 0, len(serialTypes))
	for _, t := range serialTypes {
		switch {
		case t == 0:
			values = append(values, nil)
		case t >= 1 && t <= 6:
			size := []int{1, 2, 3, 4, 6, 8}[t-1]
			if len(body) < size {
				return nil, fmt.Errorf("sqlite record body is truncated")
			}
			values = append(values, sqliteSignedInt(body[:size]))
			body = body[size:]
		case t == 7:
			if len(body) < 8 {
				return nil, fmt.Errorf("sqlite record body is truncated")
			}
			values = append(values, math.Float64frombits(binary.BigEndian.Uint64(body[:8])))
			body = body[8:]
		case t == 8:
			values = append(values, int64(0))
		case t == 9:
			values = append(values, int64(1))
		case t == 10 || t == 11:
			return nil, fmt.Errorf("sqlite serial type %d is reserved", t)
		case t%2 == 0:
			size := int((t - 12) / 2)
			if len(body) < size {
				return nil, fmt.Errorf("sqlite record body is truncated")
			}
			blob := make([]byte, size)
			copy(blob, body[:size])
			values = append(values, blob)
			body = body[size:]
		default:
			size := int((t - 13) / 2)
			if len(body) < size {
				return nil, fmt.Errorf("sqlite record body is truncated")
			}
			values = append(values, string(body[:size]))
			body = body[size:]
		}
	}
	return values, nil
}

// sqliteSignedInt decodes a 1-8 byte two's-complement big-endian integer.
func sqliteSignedInt(b []byte) int64 {
	var value int64
	for _, c := range b {
		value = value<<8 | int64(c)
	}
	shift := uint(64 - 8*len(b))
	return value << shift >> shift
}

func sqliteInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}
