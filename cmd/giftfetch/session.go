package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/session"
)

// resolveSessionPath returns the path that should be handed to
// telegram.FileSessionStorage. Telethon stores its sessions as SQLite
// databases, which the td runtime cannot read: giftfetch detects the SQLite
// magic, converts the stored authorization into the td JSON session format
// and caches it next to the original file as <name>.td.session. The original
// Telethon session is never modified. Plain td JSON sessions are used as-is.
func resolveSessionPath(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read session %q: %w", path, err)
	}
	if !isSQLiteDatabase(raw) {
		return path, nil
	}
	converted := strings.TrimSuffix(path, ".session") + ".td.session"
	if data, err := os.ReadFile(converted); err == nil && validTDSession(data) {
		fmt.Printf("[session] reusing converted session %s\n", converted)
		return converted, nil
	}
	data, err := convertTelethonSession(raw)
	if err != nil {
		return "", fmt.Errorf("convert telethon session %q: %w", path, err)
	}
	if err := os.WriteFile(converted, data, 0o600); err != nil {
		return "", fmt.Errorf("write converted session %q: %w", converted, err)
	}
	fmt.Printf("[session] converted telethon session %s -> %s\n", path, converted)
	return converted, nil
}

const sqliteHeaderMagic = "SQLite format 3\x00"

func isSQLiteDatabase(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte(sqliteHeaderMagic))
}

type tdSessionEnvelope struct {
	Version int
	Data    session.Data
}

func validTDSession(data []byte) bool {
	var envelope tdSessionEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return false
	}
	return envelope.Version == 1 && len(envelope.Data.AuthKey) == 256 && envelope.Data.Addr != ""
}

func convertTelethonSession(raw []byte) ([]byte, error) {
	dcID, serverAddress, port, authKey, err := readTelethonSessionRow(raw)
	if err != nil {
		return nil, err
	}
	if len(authKey) != 256 {
		return nil, fmt.Errorf("auth key has %d bytes, want 256", len(authKey))
	}
	if serverAddress == "" {
		return nil, fmt.Errorf("sessions row has no server address; connect once with Telethon before dumping")
	}
	var key crypto.Key
	copy(key[:], authKey)
	keyID := key.ID()
	encoded, err := json.Marshal(tdSessionEnvelope{
		Version: 1,
		Data: session.Data{
			DC:        dcID,
			Addr:      net.JoinHostPort(serverAddress, strconv.Itoa(port)),
			AuthKey:   key[:],
			AuthKeyID: keyID[:],
		},
	})
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// ---------------------------------------------------------------------------
// Minimal read-only SQLite support sufficient for Telethon session files:
// page/b-tree traversal of ordinary table b-trees plus record decoding.
// ---------------------------------------------------------------------------

const (
	sqlitePageLeafTable     = 0x0d
	sqlitePageInteriorTable = 0x05
)

type sqliteValue struct {
	kind  byte // 0 = null, 1 = integer, 2 = text, 3 = blob
	int64 int64
	text  string
	blob  []byte
}

func (v sqliteValue) isNull() bool { return v.kind == 0 }

type sqliteRecord struct {
	rowid  int64
	values []sqliteValue
}

func sqlitePageSize(raw []byte) (int, error) {
	if len(raw) < 100 || !isSQLiteDatabase(raw) {
		return 0, fmt.Errorf("not a SQLite database")
	}
	size := int(binary.BigEndian.Uint16(raw[16:18]))
	if size == 1 {
		size = 65536
	}
	if size < 512 || size&(size-1) != 0 {
		return 0, fmt.Errorf("unsupported SQLite page size %d", size)
	}
	return size, nil
}

func sqlitePage(raw []byte, pageSize, page int) ([]byte, error) {
	if page < 1 {
		return nil, fmt.Errorf("invalid SQLite page %d", page)
	}
	start := (page - 1) * pageSize
	if start+pageSize > len(raw) {
		return nil, fmt.Errorf("SQLite page %d is out of bounds", page)
	}
	return raw[start : start+pageSize], nil
}

// sqliteTableRecords walks the table b-tree rooted at rootPage and returns
// every stored record in rowid order.
func sqliteTableRecords(raw []byte, pageSize, rootPage int) ([]sqliteRecord, error) {
	var records []sqliteRecord
	visited := make(map[int]struct{})
	var walk func(page int) error
	walk = func(page int) error {
		if _, seen := visited[page]; seen {
			return fmt.Errorf("SQLite b-tree cycle at page %d", page)
		}
		visited[page] = struct{}{}
		data, err := sqlitePage(raw, pageSize, page)
		if err != nil {
			return err
		}
		header := 0
		if page == 1 {
			header = 100
		}
		pageType := data[header]
		cellCount := int(binary.BigEndian.Uint16(data[header+3 : header+5]))
		switch pageType {
		case sqlitePageLeafTable:
			pointers := header + 8
			for i := 0; i < cellCount; i++ {
				cellOffset := int(binary.BigEndian.Uint16(data[pointers+2*i : pointers+2*i+2]))
				if cellOffset < 0 || cellOffset >= pageSize {
					return fmt.Errorf("SQLite page %d cell %d out of bounds", page, i)
				}
				record, err := sqliteLeafCell(raw, pageSize, data, cellOffset)
				if err != nil {
					return err
				}
				records = append(records, record)
			}
		case sqlitePageInteriorTable:
			pointers := header + 12
			for i := 0; i < cellCount; i++ {
				cellOffset := int(binary.BigEndian.Uint16(data[pointers+2*i : pointers+2*i+2]))
				if cellOffset < 0 || cellOffset+4 > pageSize {
					return fmt.Errorf("SQLite page %d cell %d out of bounds", page, i)
				}
				child := int(binary.BigEndian.Uint32(data[cellOffset : cellOffset+4]))
				if err := walk(child); err != nil {
					return err
				}
			}
			rightMost := int(binary.BigEndian.Uint32(data[header+8 : header+12]))
			if err := walk(rightMost); err != nil {
				return err
			}
		default:
			return fmt.Errorf("SQLite page %d has unsupported b-tree type 0x%02x", page, pageType)
		}
		return nil
	}
	if err := walk(rootPage); err != nil {
		return nil, err
	}
	return records, nil
}

func sqliteLeafCell(raw []byte, pageSize int, page []byte, offset int) (sqliteRecord, error) {
	payloadLen, used, err := sqliteVarint(page[offset:])
	if err != nil {
		return sqliteRecord{}, err
	}
	rowid, used2, err := sqliteVarint(page[offset+used:])
	if err != nil {
		return sqliteRecord{}, err
	}
	payloadStart := offset + used + used2
	usable := pageSize // Telethon sessions do not use reserved page space
	maxLocal := usable - 35
	var payload []byte
	if int(payloadLen) <= maxLocal {
		if payloadStart+int(payloadLen) > pageSize {
			return sqliteRecord{}, fmt.Errorf("SQLite leaf cell payload out of bounds")
		}
		payload = page[payloadStart : payloadStart+int(payloadLen)]
	} else {
		minLocal := (usable-12)*32/255 - 23
		local := minLocal + (int(payloadLen)-minLocal)%(usable-4)
		if local > maxLocal {
			local = minLocal
		}
		if payloadStart+local+4 > pageSize {
			return sqliteRecord{}, fmt.Errorf("SQLite leaf cell overflow pointer out of bounds")
		}
		payload = append([]byte(nil), page[payloadStart:payloadStart+local]...)
		next := int(binary.BigEndian.Uint32(page[payloadStart+local : payloadStart+local+4]))
		remaining := int(payloadLen) - local
		for remaining > 0 {
			if next == 0 {
				return sqliteRecord{}, fmt.Errorf("SQLite overflow chain ended early")
			}
			overflow, err := sqlitePage(raw, pageSize, next)
			if err != nil {
				return sqliteRecord{}, err
			}
			next = int(binary.BigEndian.Uint32(overflow[0:4]))
			chunk := overflow[4:]
			if len(chunk) > remaining {
				chunk = chunk[:remaining]
			}
			payload = append(payload, chunk...)
			remaining -= len(chunk)
		}
	}
	return sqliteDecodeRecord(payloadLen, rowid, payload)
}

func sqliteDecodeRecord(payloadLen int64, rowid int64, payload []byte) (sqliteRecord, error) {
	headerLen, used, err := sqliteVarint(payload)
	if err != nil {
		return sqliteRecord{}, err
	}
	if headerLen <= 0 || int(headerLen) > len(payload) {
		return sqliteRecord{}, fmt.Errorf("SQLite record header %d out of bounds", headerLen)
	}
	var serials []int64
	offset := used
	for offset < int(headerLen) {
		serial, consumed, err := sqliteVarint(payload[offset:])
		if err != nil {
			return sqliteRecord{}, err
		}
		offset += consumed
		serials = append(serials, serial)
	}
	record := sqliteRecord{rowid: rowid}
	body := int(headerLen)
	for _, serial := range serials {
		value, size, err := sqliteSerialValue(serial, payload[body:])
		if err != nil {
			return sqliteRecord{}, err
		}
		body += size
		record.values = append(record.values, value)
	}
	if int64(len(payload)) != payloadLen {
		return sqliteRecord{}, fmt.Errorf("SQLite record payload mismatch: got %d want %d", len(payload), payloadLen)
	}
	return record, nil
}

func sqliteVarint(b []byte) (int64, int, error) {
	var result uint64
	for i := 0; i < len(b) && i < 9; i++ {
		if i == 8 {
			result = result<<8 | uint64(b[i])
			return int64(result), 9, nil
		}
		result = result<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return int64(result), i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("truncated SQLite varint")
}

func sqliteSerialValue(serial int64, body []byte) (sqliteValue, int, error) {
	switch serial {
	case 0:
		return sqliteValue{kind: 0}, 0, nil
	case 1, 2, 3, 4, 5, 6:
		size := map[int64]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 6, 6: 8}[serial]
		if len(body) < size {
			return sqliteValue{}, 0, fmt.Errorf("SQLite integer truncated")
		}
		var result int64
		for i := 0; i < size; i++ {
			result = result<<8 | int64(body[i])
		}
		result = signExtend(result, size)
		return sqliteValue{kind: 1, int64: result}, size, nil
	case 7:
		return sqliteValue{}, 0, fmt.Errorf("SQLite float columns are not supported")
	case 8:
		return sqliteValue{kind: 1}, 0, nil
	case 9:
		return sqliteValue{kind: 1, int64: 1}, 0, nil
	}
	if serial < 12 {
		return sqliteValue{}, 0, fmt.Errorf("unsupported SQLite serial type %d", serial)
	}
	length := int(serial-12) / 2
	if serial%2 == 0 {
		if len(body) < length {
			return sqliteValue{}, 0, fmt.Errorf("SQLite blob truncated")
		}
		return sqliteValue{kind: 3, blob: append([]byte(nil), body[:length]...)}, length, nil
	}
	if len(body) < length {
		return sqliteValue{}, 0, fmt.Errorf("SQLite text truncated")
	}
	return sqliteValue{kind: 2, text: string(body[:length])}, length, nil
}

func signExtend(value int64, size int) int64 {
	shift := uint(64 - 8*size)
	if shift >= 64 {
		return value
	}
	return value << shift >> shift
}

// readTelethonSessionRow locates the sessions table via sqlite_master and
// returns the columns Telethon stores: dc_id, server_address, port and
// auth_key. Columns are resolved by name from the CREATE TABLE statement so
// that column order changes across Telethon versions do not matter.
func readTelethonSessionRow(raw []byte) (dcID int, serverAddress string, port int, authKey []byte, err error) {
	pageSize, err := sqlitePageSize(raw)
	if err != nil {
		return 0, "", 0, nil, err
	}
	records, err := sqliteTableRecords(raw, pageSize, 1)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("read sqlite_master: %w", err)
	}
	var rootPage int64
	var createSQL string
	for _, record := range records {
		if len(record.values) < 5 || record.values[0].isNull() || record.values[1].isNull() {
			continue
		}
		if record.values[0].text == "table" && record.values[1].text == "sessions" {
			rootPage = record.values[3].int64
			createSQL = record.values[4].text
			break
		}
	}
	if rootPage <= 0 {
		return 0, "", 0, nil, fmt.Errorf("database has no sessions table")
	}
	columns, err := sqliteCreateTableColumns(createSQL)
	if err != nil {
		return 0, "", 0, nil, err
	}
	index := make(map[string]int, len(columns))
	for i, name := range columns {
		index[strings.ToLower(name)] = i
	}
	for _, column := range []string{"dc_id", "server_address", "port", "auth_key"} {
		if _, ok := index[column]; !ok {
			return 0, "", 0, nil, fmt.Errorf("sessions table has no %q column", column)
		}
	}
	rows, err := sqliteTableRecords(raw, pageSize, int(rootPage))
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("read sessions table: %w", err)
	}
	if len(rows) == 0 {
		return 0, "", 0, nil, fmt.Errorf("sessions table is empty")
	}
	row := rows[0]
	if len(row.values) < len(columns) {
		return 0, "", 0, nil, fmt.Errorf("sessions row has %d values, want %d", len(row.values), len(columns))
	}
	dcValue := row.values[index["dc_id"]]
	portValue := row.values[index["port"]]
	addrValue := row.values[index["server_address"]]
	keyValue := row.values[index["auth_key"]]
	if dcValue.kind != 1 || portValue.kind != 1 || addrValue.kind != 2 || keyValue.kind != 3 {
		return 0, "", 0, nil, fmt.Errorf("sessions row has unexpected column types")
	}
	if dcValue.int64 < 1 || dcValue.int64 > 5 {
		return 0, "", 0, nil, fmt.Errorf("sessions row has invalid dc_id %d", dcValue.int64)
	}
	return int(dcValue.int64), addrValue.text, int(portValue.int64), keyValue.blob, nil
}

// sqliteCreateTableColumns extracts the column names from a CREATE TABLE
// statement, skipping table-level constraints.
func sqliteCreateTableColumns(sql string) ([]string, error) {
	open := strings.Index(sql, "(")
	close := strings.LastIndex(sql, ")")
	if open < 0 || close <= open {
		return nil, fmt.Errorf("cannot parse CREATE TABLE statement %q", sql)
	}
	body := sql[open+1 : close]
	var columns []string
	var current strings.Builder
	depth := 0
	quote := byte(0)
	flush := func() {
		definition := strings.TrimSpace(current.String())
		current.Reset()
		if definition == "" {
			return
		}
		name, rest := splitFirstToken(definition)
		_ = rest
		upper := strings.ToUpper(name)
		if depth == 0 && quote == 0 {
			switch upper {
			case "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "CONSTRAINT":
				return
			}
		}
		columns = append(columns, unquoteIdentifier(name))
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			current.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			current.WriteByte(c)
		case '[':
			quote = ']'
			current.WriteByte(c)
		case '(':
			depth++
			current.WriteByte(c)
		case ')':
			depth--
			current.WriteByte(c)
		case ',':
			if depth == 0 {
				flush()
			} else {
				current.WriteByte(c)
			}
		default:
			current.WriteByte(c)
		}
	}
	flush()
	if len(columns) == 0 {
		return nil, fmt.Errorf("CREATE TABLE statement %q has no columns", sql)
	}
	return columns, nil
}

func splitFirstToken(definition string) (string, string) {
	trimmed := strings.TrimSpace(definition)
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == ' ' || trimmed[i] == '\t' || trimmed[i] == '\n' {
			return trimmed[:i], trimmed[i+1:]
		}
	}
	return trimmed, ""
}

func unquoteIdentifier(name string) string {
	if len(name) >= 2 {
		switch {
		case name[0] == '"' && name[len(name)-1] == '"',
			name[0] == '`' && name[len(name)-1] == '`',
			name[0] == '[' && name[len(name)-1] == ']':
			return name[1 : len(name)-1]
		}
	}
	return name
}
