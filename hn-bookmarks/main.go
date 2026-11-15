package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

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

// route answers exactly two requests, like the Node server this servelet replaces; every other
// method or path is 404, including a known path with another method.
func route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.RequestURI() == "/health":
		servelet.JSON(w, http.StatusOK, map[string]bool{"ready": true})
	case r.Method == http.MethodPost && r.URL.RequestURI() == "/v1/documents":
		documents(w, r)
	default:
		servelet.Fail(w, http.StatusNotFound, "NOT_FOUND", "Unknown endpoint")
	}
}

// documents runs one `get`, `list` or `commit` operation from `{ "operation", "params" }`.
func documents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, err)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if !json.Valid(trimmed) {
		fail(w, invalid("Invalid JSON"))
		return
	}
	// An array passes as an object, as in JavaScript, and then has no operation or parameters.
	if trimmed[0] != '{' && trimmed[0] != '[' {
		fail(w, invalid("Expected an object"))
		return
	}
	var input struct {
		Operation json.RawMessage `json:"operation"`
		Params    json.RawMessage `json:"params"`
	}
	if trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &input); err != nil {
			fail(w, invalid("Expected an object"))
			return
		}
	}
	answer, err := execute(input.Operation, input.Params)
	if err != nil {
		fail(w, err)
		return
	}
	servelet.JSON(w, http.StatusOK, answer)
}

// fail answers 400 for invalid input, 409 for a failed revision check and 500 otherwise.
func fail(w http.ResponseWriter, err error) {
	var api *apiError
	if !errors.As(err, &api) {
		servelet.Logf("bookmarks store: %v", err)
		servelet.Fail(w, http.StatusInternalServerError, "STORAGE_FAILURE", "Backend operation failed; reread before retrying")
		return
	}
	status := http.StatusBadRequest
	if api.code == "CONFLICT" {
		status = http.StatusConflict
	}
	servelet.Fail(w, status, api.code, api.message)
}
