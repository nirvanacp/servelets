package main

// The X library: categories, saved posts and their memberships under one revision counter, and a
// receipt for every committed operation so a retried change is answered, not applied twice. This is
// the store of the applet's former Node backend, rule for rule. The one addition is the claim: the
// Node server bound its library to a handle from its environment, while this one binds it to the
// first signed-in handle the applet sends.

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// schemaV1 is the Node backend's schema at its user_version 1. Add a new step for every change;
// never edit a step that has shipped.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS libraries(id TEXT PRIMARY KEY, expected_handle TEXT NOT NULL, revision INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS categories(library_id TEXT NOT NULL REFERENCES libraries(id), id TEXT NOT NULL,
  name TEXT NOT NULL, name_key TEXT NOT NULL, revision INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL,
  PRIMARY KEY(library_id,id), UNIQUE(library_id,name_key));
CREATE TABLE IF NOT EXISTS saved_posts(library_id TEXT NOT NULL REFERENCES libraries(id), post_id TEXT NOT NULL,
  url TEXT NOT NULL, snapshot TEXT NOT NULL, search_text TEXT NOT NULL, revision INTEGER NOT NULL,
  saved_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL, PRIMARY KEY(library_id,post_id));
CREATE TABLE IF NOT EXISTS memberships(library_id TEXT NOT NULL, post_id TEXT NOT NULL, category_id TEXT NOT NULL,
  PRIMARY KEY(library_id,post_id,category_id),
  FOREIGN KEY(library_id,post_id) REFERENCES saved_posts(library_id,post_id) ON DELETE CASCADE,
  FOREIGN KEY(library_id,category_id) REFERENCES categories(library_id,id));
CREATE INDEX IF NOT EXISTS saved_order ON saved_posts(library_id,saved_at_ms DESC,post_id DESC);
CREATE INDEX IF NOT EXISTS category_members ON memberships(library_id,category_id,post_id);
CREATE TABLE IF NOT EXISTS operations(library_id TEXT NOT NULL REFERENCES libraries(id), id TEXT NOT NULL,
  request_hash TEXT NOT NULL, receipt TEXT NOT NULL, created_at_ms INTEGER NOT NULL, PRIMARY KEY(library_id,id));
`

const (
	categoriesMax  = 50
	lookupMax      = 50
	pageSize       = 20
	pageBytesMax   = 192 * 1024
	membershipsMax = 10
)

type store struct {
	tx *sql.Tx
	// id is the library's ID, set by library.
	id string
}

type libraryRecord struct {
	Version        int    `json:"version"`
	LibraryID      string `json:"libraryId"`
	ExpectedHandle string `json:"expectedHandle"`
	Revision       int64  `json:"revision"`
}

type categoryRecord struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	NameKey     string `json:"nameKey"`
	Revision    int64  `json:"revision"`
	CreatedAtMs int64  `json:"createdAtMs"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
	Count       int64  `json:"count"`
}

type savedRecord struct {
	Version     int             `json:"version"`
	PostID      string          `json:"postId"`
	URL         string          `json:"url"`
	Snapshot    json.RawMessage `json:"snapshot"`
	Revision    int64           `json:"revision"`
	SavedAtMs   int64           `json:"savedAtMs"`
	UpdatedAtMs int64           `json:"updatedAtMs"`
	CategoryIDs []string        `json:"categoryIds"`
}

type receipt struct {
	OperationID     string `json:"operationId"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Result          any    `json:"result"`
	LibraryRevision int64  `json:"libraryRevision"`
	CreatedAtMs     int64  `json:"createdAtMs"`
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func (s *store) exec(query string, args ...any) {
	_, err := s.tx.Exec(query, args...)
	check(err)
}

// found scans one row; false when there is none.
func found(row *sql.Row, dest ...any) bool {
	err := row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	check(err)
	return true
}

func (s *store) readLibrary() (libraryRecord, bool) {
	record := libraryRecord{Version: 1}
	ok := found(s.tx.QueryRow("SELECT id, expected_handle, revision FROM libraries LIMIT 1"),
		&record.LibraryID, &record.ExpectedHandle, &record.Revision)
	return record, ok
}

// library is the claimed library; every endpoint but the claim needs one.
func (s *store) library() libraryRecord {
	record, ok := s.readLibrary()
	if !ok {
		fail("LIBRARY_MISSING", "Open the library while signed in to X", http.StatusConflict, nil)
	}
	s.id = record.LibraryID
	return record
}

func (s *store) libraryRecord() any { return s.library() }

// claim binds a new library to the signed-in handle. An existing library is returned unchanged,
// whichever handle it belongs to, so the applet can tell the user they are signed in to another
// account.
func (s *store) claim(input any) any {
	expected := handle(object(input)["expectedHandle"])
	if _, ok := s.readLibrary(); !ok {
		s.exec("INSERT INTO libraries VALUES(?,?,0)", newUUID(), expected)
	}
	return s.library()
}

// newUUID is a random version 4 UUID.
func newUUID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	check(err)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *store) category(id string) (categoryRecord, bool) {
	record := categoryRecord{Version: 1}
	ok := found(s.tx.QueryRow("SELECT id, name, name_key, revision, created_at_ms, updated_at_ms FROM categories WHERE library_id=? AND id=?", s.id, id),
		&record.ID, &record.Name, &record.NameKey, &record.Revision, &record.CreatedAtMs, &record.UpdatedAtMs)
	return record, ok
}

const savedColumns = "post_id, url, snapshot, revision, saved_at_ms, updated_at_ms"

func scanSaved(scan func(...any) error) (savedRecord, error) {
	record := savedRecord{Version: 1}
	var captured string
	err := scan(&record.PostID, &record.URL, &captured, &record.Revision, &record.SavedAtMs, &record.UpdatedAtMs)
	record.Snapshot = json.RawMessage(captured)
	return record, err
}

// saved is the saved post without its categories; savedRecord adds them.
func (s *store) saved(id string) (savedRecord, bool) {
	record, err := scanSaved(s.tx.QueryRow("SELECT "+savedColumns+" FROM saved_posts WHERE library_id=? AND post_id=?", s.id, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false
	}
	check(err)
	return record, true
}

func (s *store) memberships(id string) []string {
	rows, err := s.tx.Query("SELECT category_id FROM memberships WHERE library_id=? AND post_id=? ORDER BY category_id", s.id, id)
	check(err)
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var category string
		check(rows.Scan(&category))
		ids = append(ids, category)
	}
	check(rows.Err())
	return ids
}

func (s *store) savedRecord(id string) savedRecord {
	record, _ := s.saved(id)
	record.CategoryIDs = s.memberships(id)
	return record
}

func (s *store) categories() any {
	library := s.library()
	rows, err := s.tx.Query(`SELECT c.id, c.name, c.name_key, c.revision, c.created_at_ms, c.updated_at_ms, COUNT(m.post_id)
		FROM categories c LEFT JOIN memberships m ON c.library_id=m.library_id AND c.id=m.category_id
		WHERE c.library_id=? GROUP BY c.id ORDER BY c.name_key, c.id`, s.id)
	check(err)
	defer rows.Close()
	categories := []categoryRecord{}
	for rows.Next() {
		record := categoryRecord{Version: 1}
		check(rows.Scan(&record.ID, &record.Name, &record.NameKey, &record.Revision, &record.CreatedAtMs, &record.UpdatedAtMs, &record.Count))
		categories = append(categories, record)
	}
	check(rows.Err())
	return map[string]any{"libraryRevision": library.Revision, "categories": categories}
}

// distinct reports whether no two items are the same string, as a JavaScript Set of them would.
func distinct(items []any) bool {
	seen := map[string]bool{}
	for _, item := range items {
		if s, ok := item.(string); ok {
			if seen[s] {
				return false
			}
			seen[s] = true
		}
	}
	return true
}

type lookupRecord struct {
	PostID      string   `json:"postId"`
	Revision    int64    `json:"revision"`
	CategoryIDs []string `json:"categoryIds"`
}

func (s *store) lookup(input any) any {
	ids, ok := object(input)["postIds"].([]any)
	if !ok || len(ids) > lookupMax || !distinct(ids) {
		reject("Lookup requires up to 50 unique post IDs")
	}
	for _, id := range ids {
		postID(id)
	}
	library := s.library()
	records, missing := []lookupRecord{}, []string{}
	for _, raw := range ids {
		id := raw.(string)
		if row, ok := s.saved(id); ok {
			records = append(records, lookupRecord{id, row.Revision, s.memberships(id)})
		} else {
			missing = append(missing, id)
		}
	}
	return map[string]any{"records": records, "missingIds": missing, "libraryRevision": library.Revision}
}

type cursor struct {
	LibraryID       string `json:"libraryId"`
	FilterHash      string `json:"filterHash"`
	LibraryRevision int64  `json:"libraryRevision"`
	SavedAtMs       int64  `json:"savedAtMs"`
	PostID          string `json:"postId"`
}

type filter struct {
	CategoryID *string `json:"categoryId"`
	Query      string  `json:"query"`
}

func (s *store) query(input any) any {
	fields := object(input)
	var categoryID *string
	if fields["categoryId"] != nil {
		id := uuid(fields["categoryId"])
		categoryID = &id
	}
	rawQuery := fields["query"]
	if rawQuery == nil {
		rawQuery = ""
	}
	query := normalize(text(rawQuery, 200, 800, true))
	if limit, ok := safeInteger(fields["limit"]); !ok || limit != pageSize {
		reject("Page limit must be 20")
	}
	var hashed any
	if categoryID != nil {
		hashed = *categoryID
	}
	filterHash := digest(map[string]any{"categoryId": hashed, "query": query})
	libraryRevision := s.library().Revision
	if categoryID != nil {
		if _, ok := s.category(*categoryID); !ok {
			fail("CATEGORY_MISSING", "This category no longer exists", http.StatusConflict, nil)
		}
	}
	var after any // nil, or the savedAtMs of the cursor's last entry
	var afterID string
	if fields["cursor"] != nil {
		position := s.readCursor(fields["cursor"], filterHash, libraryRevision)
		after, afterID = position.SavedAtMs, position.PostID
	}
	savedAt := after
	if savedAt == nil {
		savedAt = int64(0)
	}
	rows, err := s.tx.Query(`SELECT `+savedColumns+` FROM saved_posts p WHERE p.library_id=?
		AND (? IS NULL OR EXISTS(SELECT 1 FROM memberships m WHERE m.library_id=p.library_id AND m.post_id=p.post_id AND m.category_id=?))
		AND instr(p.search_text,?)>0 AND (? IS NULL OR p.saved_at_ms<? OR (p.saved_at_ms=? AND p.post_id<?))
		ORDER BY p.saved_at_ms DESC, p.post_id DESC LIMIT 21`,
		s.id, hashed, hashed, query, after, savedAt, savedAt, afterID)
	check(err)
	var page []savedRecord
	for rows.Next() {
		record, err := scanSaved(rows.Scan)
		check(err)
		page = append(page, record)
	}
	check(rows.Err())
	rows.Close()
	entries, size := []savedRecord{}, 1024
	for _, record := range page {
		record.CategoryIDs = s.memberships(record.PostID)
		length := len(encoded(record))
		if len(entries) == pageSize || size+length > pageBytesMax {
			break
		}
		entries = append(entries, record)
		size += length
	}
	var next any
	if len(entries) > 0 && len(entries) < len(page) {
		last := entries[len(entries)-1]
		next = base64.RawURLEncoding.EncodeToString(encoded(cursor{s.id, filterHash, libraryRevision, last.SavedAtMs, last.PostID}))
	}
	return map[string]any{"entries": entries, "nextCursor": next, "libraryRevision": libraryRevision, "filter": filter{categoryID, query}}
}

// readCursor decodes a page cursor and checks it was made for this library, filter and revision.
func (s *store) readCursor(value any, filterHash string, libraryRevision int64) cursor {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(text(value, 2048, 8192, false), "="))
	var decoded any
	if err == nil {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		err = decoder.Decode(&decoded)
	}
	if err != nil {
		reject("Invalid cursor")
	}
	fields := object(decoded)
	stamp, ok := safeInteger(fields["libraryRevision"])
	if fields["libraryId"] != s.id || fields["filterHash"] != filterHash || !ok || stamp != libraryRevision {
		fail("CURSOR_STALE", "Saved posts changed. Refresh the list.", http.StatusConflict, nil)
	}
	id := postID(fields["postId"])
	savedAt, ok := safeInteger(fields["savedAtMs"])
	if !ok || savedAt < 0 {
		reject("Invalid cursor position")
	}
	return cursor{s.id, filterHash, libraryRevision, savedAt, id}
}

func (s *store) operation(id string) any {
	id = uuid(id)
	s.library()
	var stored string
	if !found(s.tx.QueryRow("SELECT receipt FROM operations WHERE library_id=? AND id=?", s.id, id), &stored) {
		fail("OPERATION_NOT_FOUND", "No committed operation found", http.StatusNotFound, nil)
	}
	return json.RawMessage(stored)
}

func (s *store) operate(input any) any {
	fields := object(input)
	operationID := uuid(fields["operationId"])
	payload := object(fields["payload"])
	kind, ok := oneOf(fields["kind"], "category.create", "category.rename", "category.delete", "bookmark.setCategories", "bookmark.remove")
	if !ok {
		reject("Unknown operation")
	}
	requestHash := digest(map[string]any{"kind": kind, "payload": payload})
	library := s.library()
	var storedHash, stored string
	if found(s.tx.QueryRow("SELECT request_hash, receipt FROM operations WHERE library_id=? AND id=?", s.id, operationID), &storedHash, &stored) {
		if storedHash != requestHash {
			fail("OPERATION_REUSED", "Operation ID was used for a different change", http.StatusConflict, nil)
		}
		return json.RawMessage(stored)
	}
	next, now := library.Revision+1, time.Now().UnixMilli()
	if next > safeIntegerMax {
		fail("LIMIT_REACHED", "Library revision exhausted", http.StatusServiceUnavailable, nil)
	}
	var result any
	if strings.HasPrefix(kind, "category.") {
		result = s.changeCategory(kind, payload, next, now)
	} else {
		result = s.changeBookmark(kind, payload, next, now)
	}
	s.exec("UPDATE libraries SET revision=? WHERE id=?", next, s.id)
	committed := receipt{operationID, kind, "committed", result, next, now}
	s.exec("INSERT INTO operations VALUES(?,?,?,?,?)", s.id, operationID, requestHash, string(encoded(committed)), now)
	return committed
}

func (s *store) changeCategory(kind string, payload map[string]any, next, now int64) any {
	id := uuid(payload["id"])
	row, exists := s.category(id)
	if kind == "category.create" {
		if exists {
			fail("CONFLICT", "Category already exists. Refresh before changing it.", http.StatusConflict, nil)
		}
		var count int64
		found(s.tx.QueryRow("SELECT COUNT(*) FROM categories WHERE library_id=?", s.id), &count)
		if count >= categoriesMax {
			fail("LIMIT_REACHED", "You can create up to 50 categories", http.StatusConflict, nil)
		}
	} else if !exists || row.Revision != *revision(payload["expectedRevision"], false) {
		fail("CONFLICT", "Category changed. Refresh before changing it.", http.StatusConflict, nil)
	}
	if kind == "category.delete" {
		var one int64
		if found(s.tx.QueryRow("SELECT 1 FROM memberships WHERE library_id=? AND category_id=? LIMIT 1", s.id, id), &one) {
			fail("CATEGORY_NOT_EMPTY", "Move or remove saved posts before deleting this category", http.StatusConflict, nil)
		}
		s.exec("DELETE FROM categories WHERE library_id=? AND id=?", s.id, id)
		return map[string]any{"id": id, "removed": true}
	}
	name, nameKey := categoryName(payload["name"])
	var duplicate string
	if found(s.tx.QueryRow("SELECT id FROM categories WHERE library_id=? AND name_key=? AND id<>?", s.id, nameKey, id), &duplicate) {
		fail("CATEGORY_NAME_EXISTS", "A category with this name already exists", http.StatusConflict, map[string]string{"existingCategoryId": duplicate})
	}
	if kind == "category.create" {
		s.exec("INSERT INTO categories VALUES(?,?,?,?,?,?,?)", s.id, id, name, nameKey, next, now, now)
	} else {
		s.exec("UPDATE categories SET name=?, name_key=?, revision=?, updated_at_ms=? WHERE library_id=? AND id=?", name, nameKey, next, now, s.id, id)
	}
	// The Node store answered without the member count, which reads as 0.
	record, _ := s.category(id)
	return record
}

func (s *store) changeBookmark(kind string, payload map[string]any, next, now int64) any {
	id := postID(payload["postId"])
	row, exists := s.saved(id)
	expected := revision(payload["expectedRevision"], true)
	if exists != (expected != nil) || exists && row.Revision != *expected {
		fail("CONFLICT", "Saved post changed. Refresh before changing it.", http.StatusConflict, nil)
	}
	if kind == "bookmark.remove" {
		if !exists {
			fail("CONFLICT", "Saved post no longer exists", http.StatusConflict, nil)
		}
		s.exec("DELETE FROM saved_posts WHERE library_id=? AND post_id=?", s.id, id)
		return map[string]any{"postId": id, "removed": true}
	}
	raw, ok := payload["categoryIds"].([]any)
	if !ok || len(raw) < 1 || len(raw) > membershipsMax || !distinct(raw) {
		reject("Choose between 1 and 10 categories")
	}
	ids := make([]any, len(raw))
	for i, category := range raw {
		ids[i] = uuid(category)
	}
	if !distinct(ids) {
		reject("Duplicate category membership")
	}
	for _, category := range ids {
		if _, ok := s.category(category.(string)); !ok {
			fail("CATEGORY_MISSING", "A selected category no longer exists", http.StatusConflict, nil)
		}
	}
	if !exists {
		post := canonicalURL(payload["url"], id)
		captured := snapshot(payload["snapshot"], post.handle)
		searchText := normalize(captured.AuthorHandle + " " + captured.AuthorName + " " + captured.Text)
		s.exec("INSERT INTO saved_posts VALUES(?,?,?,?,?,?,?,?)", s.id, id, post.url, string(encoded(captured)), searchText, next, now, now)
	} else {
		s.exec("UPDATE saved_posts SET revision=?, updated_at_ms=? WHERE library_id=? AND post_id=?", next, now, s.id, id)
	}
	s.exec("DELETE FROM memberships WHERE library_id=? AND post_id=?", s.id, id)
	for _, category := range ids {
		s.exec("INSERT INTO memberships VALUES(?,?,?)", s.id, id, category)
	}
	return s.savedRecord(id)
}
