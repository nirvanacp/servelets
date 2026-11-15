package main

// The bookmarks store: JSON documents in five collections under one revision counter. A commit
// checks the revision of every document it changes and raises the counter once. This is the
// store of the applet's former Node backend, rule for rule, so the applet's code is unchanged.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/nirvanacp/let/go/servelet"
)

// schemaV1 is the Node backend's schema at its user_version 1. Add a new step for every change;
// never edit a step that has shipped.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
INSERT OR IGNORE INTO state VALUES(1,0);
CREATE TABLE IF NOT EXISTS documents(collection TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL,
  revision INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, PRIMARY KEY(collection,key));
`

var collections = map[string]bool{
	"bookmarks": true, "bookmark-order": true, "categories": true, "category-meta": true, "category-names": true,
}

const (
	keyBytesMax      = 512
	documentBytesMax = 32768
	listBytesMax     = 240000
	checksMax        = 128
	changesMax       = 64
	safeIntegerMax   = 1<<53 - 1
)

type apiError struct{ code, message string }

func (e *apiError) Error() string { return e.code + ": " + e.message }

func invalid(message string) error { return &apiError{"INVALID_ARGUMENT", message} }

var errKey = invalid("Invalid HN document key")

type document struct {
	Key         string          `json:"key"`
	Value       json.RawMessage `json:"value"`
	Revision    int64           `json:"revision"`
	UpdatedAtMs int64           `json:"updatedAtMs"`
}

type answer struct {
	Result  any         `json:"result"`
	Changes []changeKey `json:"changes"`
}

type changeKey struct {
	Collection string `json:"collection"`
	Key        string `json:"key"`
}

// fields reads a JSON object; anything else has no fields, as a JavaScript property read of a
// non-object finds nothing.
func fields(raw json.RawMessage) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	return object
}

// present reports whether a field is set to something other than null.
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func text(raw json.RawMessage) (string, bool) {
	var value string
	if raw == nil || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// integer reads a JSON number with no fraction, as Number.isInteger accepts it.
func integer(raw json.RawMessage) (float64, bool) {
	var value float64
	if raw == nil || json.Unmarshal(raw, &value) != nil || value != math.Trunc(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// documentKey checks a `{ collection, key }` reference.
func documentKey(raw json.RawMessage) (changeKey, error) {
	object := fields(raw)
	collection, _ := text(object["collection"])
	key, ok := text(object["key"])
	if object == nil || !collections[collection] || !ok || key == "" || len(key) > keyBytesMax {
		return changeKey{}, errKey
	}
	return changeKey{collection, key}, nil
}

// encoded is the JSON text JavaScript's JSON.stringify writes for value, without HTML escapes.
func encoded(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func execute(operation, params json.RawMessage) (answer, error) {
	if !present(params) || (params[0] != '{' && params[0] != '[') {
		return answer{}, invalid("Missing parameters")
	}
	tx, err := servelet.DB().Begin()
	if err != nil {
		return answer{}, err
	}
	result, err := run(tx, operation, params)
	if err != nil {
		_ = tx.Rollback()
		return answer{}, err
	}
	if err := tx.Commit(); err != nil {
		return answer{}, err
	}
	return result, nil
}

func run(tx *sql.Tx, operation, params json.RawMessage) (answer, error) {
	var revision int64
	if err := tx.QueryRow("SELECT revision FROM state WHERE id=1").Scan(&revision); err != nil {
		return answer{}, err
	}
	name, _ := text(operation)
	switch name {
	case "get":
		return get(tx, params, revision)
	case "list":
		return list(tx, params, revision)
	case "commit":
		return commit(tx, params, revision)
	}
	return answer{}, invalid("Unknown HN operation")
}

// read returns the document, or nil when it does not exist.
func read(tx *sql.Tx, key changeKey) (*document, error) {
	var found document
	var value string
	err := tx.QueryRow("SELECT key,value,revision,updated_at_ms FROM documents WHERE collection=? AND key=?", key.Collection, key.Key).
		Scan(&found.Key, &value, &found.Revision, &found.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	found.Value = json.RawMessage(value)
	return &found, nil
}

func get(tx *sql.Tx, params json.RawMessage, revision int64) (answer, error) {
	key, err := documentKey(params)
	if err != nil {
		return answer{}, err
	}
	found, err := read(tx, key)
	if err != nil {
		return answer{}, err
	}
	return answer{Result: map[string]any{"document": found, "namespaceRevision": revision}, Changes: []changeKey{}}, nil
}

func list(tx *sql.Tx, params json.RawMessage, revision int64) (answer, error) {
	object := fields(params)
	// A missing afterKey is checked as the placeholder key "validation", as the Node store does.
	check := map[string]json.RawMessage{"collection": object["collection"], "key": object["afterKey"]}
	if !present(object["afterKey"]) {
		check["key"] = json.RawMessage(`"validation"`)
	}
	reference, _ := json.Marshal(check)
	if _, err := documentKey(reference); err != nil {
		return answer{}, err
	}
	limit, ok := integer(object["limit"])
	if !ok || limit < 1 || limit > 100 {
		return answer{}, invalid("List limit must be 1 to 100")
	}
	collection, _ := text(object["collection"])
	var after any
	if present(object["afterKey"]) {
		after, _ = text(object["afterKey"])
	}
	rows, err := tx.Query("SELECT key,value,revision,updated_at_ms FROM documents WHERE collection=? AND (? IS NULL OR key>?) ORDER BY key COLLATE BINARY LIMIT ?",
		collection, after, after, int64(limit)+1)
	if err != nil {
		return answer{}, err
	}
	defer rows.Close()
	entries, count, size := []document{}, 0, 512
	for rows.Next() {
		var entry document
		var value string
		if err := rows.Scan(&entry.Key, &value, &entry.Revision, &entry.UpdatedAtMs); err != nil {
			return answer{}, err
		}
		count++
		entry.Value = json.RawMessage(value)
		line, err := encoded(entry)
		if err != nil {
			return answer{}, err
		}
		if len(entries) == int(limit) || size+len(line)+1 > listBytesMax {
			break
		}
		entries = append(entries, entry)
		size += len(line) + 1
	}
	if err := rows.Err(); err != nil {
		return answer{}, err
	}
	// Rows were left over when the loop stopped before the end; the Node store counts every row
	// it fetched, so count the rest too.
	for rows.Next() {
		count++
	}
	var next any
	if count > len(entries) {
		if len(entries) == 0 {
			return answer{}, errors.New("a document is larger than one list page")
		}
		next = entries[len(entries)-1].Key
	}
	return answer{Result: map[string]any{"entries": entries, "nextAfterKey": next, "namespaceRevision": revision}, Changes: []changeKey{}}, nil
}

func commit(tx *sql.Tx, params json.RawMessage, revision int64) (answer, error) {
	object := fields(params)
	var checks, puts, deletes []json.RawMessage
	arrays := json.Unmarshal(object["checks"], &checks) == nil && checks != nil &&
		json.Unmarshal(object["puts"], &puts) == nil && puts != nil &&
		json.Unmarshal(object["deletes"], &deletes) == nil && deletes != nil
	if !arrays || len(checks) > checksMax || len(puts)+len(deletes) > changesMax || len(puts)+len(deletes) == 0 {
		return answer{}, invalid("Invalid transaction size")
	}
	checked := map[changeKey]bool{}
	for _, raw := range checks {
		key, err := documentKey(raw)
		if err != nil {
			return answer{}, err
		}
		expected := fields(raw)["expectedRevision"]
		var want *int64
		if string(expected) != "null" {
			number, ok := integer(expected)
			if !ok || number <= 0 || number > safeIntegerMax {
				return answer{}, invalid("Invalid revision check")
			}
			value := int64(number)
			want = &value
		}
		if checked[key] {
			return answer{}, invalid("Invalid revision check")
		}
		checked[key] = true
		found, err := read(tx, key)
		if err != nil {
			return answer{}, err
		}
		if (found == nil) != (want == nil) || (found != nil && found.Revision != *want) {
			return answer{}, &apiError{"CONFLICT", "Data changed. Reload before retrying."}
		}
	}
	modified := map[changeKey]bool{}
	changes := []changeKey{}
	for _, raw := range append(append([]json.RawMessage{}, puts...), deletes...) {
		key, err := documentKey(raw)
		if err != nil {
			return answer{}, err
		}
		if modified[key] || !checked[key] {
			return answer{}, invalid("Every modification needs a unique revision check")
		}
		modified[key] = true
		changes = append(changes, key)
	}
	values := make([]string, len(puts))
	for i, raw := range puts {
		value, ok := fields(raw)["value"]
		var compact bytes.Buffer
		if !ok || json.Compact(&compact, value) != nil || compact.Len() > documentBytesMax {
			return answer{}, invalid("Document exceeds 32 KiB or is missing")
		}
		values[i] = compact.String()
	}
	next := revision + 1
	if next > safeIntegerMax {
		return answer{}, invalid("Revision exhausted")
	}
	now := time.Now().UnixMilli()
	if _, err := tx.Exec("UPDATE state SET revision=? WHERE id=1", next); err != nil {
		return answer{}, err
	}
	for i, value := range values {
		key := changes[i]
		_, err := tx.Exec("INSERT INTO documents VALUES(?,?,?,?,?) ON CONFLICT(collection,key) DO UPDATE SET value=excluded.value,revision=excluded.revision,updated_at_ms=excluded.updated_at_ms",
			key.Collection, key.Key, value, next, now)
		if err != nil {
			return answer{}, err
		}
	}
	for _, key := range changes[len(puts):] {
		if _, err := tx.Exec("DELETE FROM documents WHERE collection=? AND key=?", key.Collection, key.Key); err != nil {
			return answer{}, err
		}
	}
	return answer{Result: map[string]any{"namespaceRevision": next}, Changes: changes}, nil
}
