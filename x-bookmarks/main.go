package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/nirvanacp/let/go/servelet"
)

func init() {
	if err := servelet.Migrate(schemaV1); err != nil {
		panic(err)
	}
	servelet.Serve(http.HandlerFunc(route))
}

// main stays empty: the host calls the handler that init registered.
func main() {}

const operationsPath = "/v1/operations/"

// route answers the library endpoints of the Node server this servelet replaces, plus
// `POST /v1/library`, which binds a new library to the signed-in X handle. Every other method or
// path is 404, including a known path with another method.
func route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.RequestURI()
	get := r.Method == http.MethodGet
	post := r.Method == http.MethodPost
	switch {
	case get && path == "/health":
		servelet.JSON(w, http.StatusOK, map[string]bool{"ready": true})
	case get && path == "/v1/library":
		answer(w, func(s *store) any { return s.libraryRecord() })
	case post && path == "/v1/library":
		withInput(w, r, func(s *store, input any) any { return s.claim(input) })
	case get && path == "/v1/categories":
		answer(w, func(s *store) any { return s.categories() })
	case get && strings.HasPrefix(path, operationsPath):
		answer(w, func(s *store) any { return s.operation(strings.TrimPrefix(path, operationsPath)) })
	case post && path == "/v1/bookmarks/lookup":
		withInput(w, r, func(s *store, input any) any { return s.lookup(input) })
	case post && path == "/v1/bookmarks/query":
		withInput(w, r, func(s *store, input any) any { return s.query(input) })
	case post && path == "/v1/operations":
		withInput(w, r, func(s *store, input any) any { return s.operate(input) })
	default:
		reply(w, &apiError{"NOT_FOUND", "Unknown library endpoint", http.StatusNotFound, nil})
	}
}

// withInput reads the JSON body, then answers like answer.
func withInput(w http.ResponseWriter, r *http.Request, handle func(*store, any) any) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		reply(w, err)
		return
	}
	if len(body) > requestBytesMax {
		reply(w, &apiError{"TOO_LARGE", "Request exceeds 256 KiB", http.StatusRequestEntityTooLarge, nil})
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var input any
	if err := decoder.Decode(&input); err != nil || decoder.More() {
		reply(w, invalid("Invalid JSON"))
		return
	}
	answer(w, func(s *store) any { return handle(s, input) })
}

// answer runs handle in one transaction and writes `{ "result" }`. A failed rule panics with an
// *apiError, as the Node store threw one; the transaction is then rolled back.
func answer(w http.ResponseWriter, handle func(*store) any) {
	tx, err := servelet.DB().Begin()
	if err != nil {
		reply(w, err)
		return
	}
	result, err := run(&store{tx: tx}, handle)
	if err != nil {
		_ = tx.Rollback()
		reply(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		reply(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func run(s *store, handle func(*store) any) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if failure, ok := r.(error); ok {
				err = failure
				return
			}
			panic(r)
		}
	}()
	return handle(s), nil
}

// reply writes an error: the rule's own status and code, or 503 STORAGE_FAILURE for anything else,
// which the applet treats as a change whose outcome is unknown.
func reply(w http.ResponseWriter, err error) {
	var api *apiError
	if !errors.As(err, &api) {
		servelet.Logf("x library: %v", err)
		api = &apiError{"STORAGE_FAILURE", "Library unavailable. Check whether the change saved before retrying.", http.StatusServiceUnavailable, nil}
	}
	body := map[string]any{"code": api.code, "message": api.message}
	if api.details != nil {
		body["details"] = api.details
	}
	writeJSON(w, api.status, map[string]any{"error": body})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded(value))
}
