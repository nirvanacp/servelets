package main

// Input rules of the X library, ported from the applet's former Node backend. Requests are decoded
// with json.Number, so a value is nil, bool, json.Number, string, []any or map[string]any, and each
// rule reads it the way the JavaScript code read the parsed JSON.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	requestBytesMax  = 256 * 1024
	snapshotBytesMax = 32768
	safeIntegerMax   = 1<<53 - 1
)

type apiError struct {
	code, message string
	status        int
	details       any
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func invalid(message string) *apiError {
	return &apiError{"INVALID_ARGUMENT", message, http.StatusBadRequest, nil}
}

// fail stops the request with a rule failure; answer turns it into the error reply.
func fail(code, message string, status int, details any) {
	panic(&apiError{code, message, status, details})
}

func reject(message string) { panic(invalid(message)) }

func object(value any) map[string]any {
	fields, ok := value.(map[string]any)
	if !ok {
		reject("Expected an object")
	}
	return fields
}

// isSpace is JavaScript's whitespace, which `trim()` and the `\s` class match.
func isSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func trim(value string) string { return strings.TrimFunc(value, isSpace) }

// collapse trims and turns each run of whitespace into one space.
func collapse(value string) string { return strings.Join(strings.FieldsFunc(value, isSpace), " ") }

// text checks a string's length in code points and UTF-8 bytes; it must have a visible character
// unless empty is set.
func text(value any, max, bytes int, empty bool) string {
	s, ok := value.(string)
	if !ok || utf8.RuneCountInString(s) > max || len(s) > bytes || !empty && trim(s) == "" {
		reject("Invalid text length")
	}
	return s
}

func normalize(value string) string { return strings.ToLower(collapse(norm.NFKC.String(value))) }

var controlCharacter = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func categoryName(value any) (name, nameKey string) {
	name = collapse(norm.NFKC.String(text(value, 320, 1280, false)))
	text(name, 80, 320, false)
	if controlCharacter.MatchString(name) {
		reject("Category names cannot contain control characters")
	}
	return name, normalize(name)
}

var (
	uuidPattern   = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	postIDPattern = regexp.MustCompile(`^[0-9]{1,25}$`)
	handlePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	statusPath    = regexp.MustCompile(`^/([A-Za-z0-9_]{1,15})/status/([0-9]{1,25})$`)
)

func uuid(value any) string {
	s, ok := value.(string)
	if !ok || !uuidPattern.MatchString(s) {
		reject("Invalid operation or category ID")
	}
	return strings.ToLower(s)
}

func postID(value any) string {
	s, ok := value.(string)
	if !ok || !postIDPattern.MatchString(s) {
		reject("Invalid post ID")
	}
	return s
}

func handle(value any) string {
	s, ok := value.(string)
	if !ok || !handlePattern.MatchString(s) {
		reject("Invalid account handle")
	}
	return strings.ToLower(s)
}

// safeInteger reads a JSON number that JavaScript's Number.isSafeInteger accepts.
func safeInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(number), 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > safeIntegerMax {
		return 0, false
	}
	return int64(f), true
}

// revision reads a record revision; null means "no record yet" when absent is set.
func revision(value any, absent bool) *int64 {
	if absent && value == nil {
		return nil
	}
	n, ok := safeInteger(value)
	if !ok || n < 1 {
		reject("Invalid record revision")
	}
	return &n
}

// canonical is a post URL rewritten to https://x.com/<handle>/status/<id>.
type canonical struct{ url, handle string }

func canonicalURL(value any, id string) canonical {
	u, err := url.Parse(trim(text(value, 256, 256, false)))
	if err != nil {
		reject("Invalid post URL")
	}
	parts := statusPath.FindStringSubmatch(u.EscapedPath())
	if u.Scheme != "https" || webHost(u) != "x.com" || u.User != nil || parts == nil || parts[2] != id {
		reject("Post URL does not match its ID")
	}
	return canonical{"https://x.com/" + parts[1] + "/status/" + id, handle(parts[1])}
}

// webHost is the URL's host as a browser reports it: lowercase, without the scheme's default port.
func webHost(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if u.Scheme == "https" {
		return strings.TrimSuffix(host, ":443")
	}
	return strings.TrimSuffix(host, ":80")
}

type media struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type link struct {
	Text string `json:"text"`
	Href string `json:"href"`
}

type snapshotRecord struct {
	AuthorHandle string `json:"authorHandle"`
	AuthorName   string `json:"authorName"`
	Text         string `json:"text"`
	TextState    string `json:"textState"`
	CapturedAtMs int64  `json:"capturedAtMs"`
	Media        media  `json:"media"`
	Links        []link `json:"links"`
	QuotedPostID string `json:"quotedPostId,omitempty"`
}

func oneOf(value any, choices ...string) (string, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	for _, choice := range choices {
		if s == choice {
			return s, true
		}
	}
	return "", false
}

// snapshot checks what the applet captured of a post when it was first saved.
func snapshot(value any, author string) snapshotRecord {
	fields := object(value)
	authorHandle := handle(fields["authorHandle"])
	if authorHandle != author {
		reject("Snapshot author does not match the post")
	}
	textState, ok := oneOf(fields["textState"], "complete", "excerpt", "unavailable")
	if !ok {
		reject("Invalid text state")
	}
	captured, ok := safeInteger(fields["capturedAtMs"])
	if !ok || captured < 0 || captured > time.Now().UnixMilli()+300000 {
		reject("Invalid capture time")
	}
	mediaFields := object(fields["media"])
	kind, ok := oneOf(mediaFields["kind"], "none", "photo", "video", "mixed")
	count, counted := safeInteger(mediaFields["count"])
	if !ok || !counted || count < 0 || count > 16 {
		reject("Invalid media metadata")
	}
	var rawLinks []any
	if fields["links"] != nil {
		rawLinks, ok = fields["links"].([]any)
		if !ok || len(rawLinks) > 8 {
			reject("Too many post links")
		}
	}
	result := snapshotRecord{
		AuthorHandle: authorHandle,
		AuthorName:   text(fields["authorName"], 200, 800, false),
		Text:         text(fields["text"], 8000, 32768, true),
		TextState:    textState,
		CapturedAtMs: captured,
		Media:        media{kind, count},
		Links:        make([]link, 0, len(rawLinks)),
	}
	for _, raw := range rawLinks {
		result.Links = append(result.Links, postLink(raw))
	}
	if quoted, ok := fields["quotedPostId"]; ok {
		result.QuotedPostID = postID(quoted)
	}
	if len(encoded(result)) > snapshotBytesMax {
		reject("Snapshot exceeds 32 KiB")
	}
	return result
}

func postLink(value any) link {
	fields := object(value)
	u, err := url.Parse(trim(text(fields["href"], 2048, 8192, false)))
	if err != nil || u.Host == "" {
		reject("Invalid post link")
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
		reject("Unsafe post link")
	}
	u.Host = webHost(u)
	if u.Path == "" {
		u.Path = "/"
	}
	return link{Text: text(fields["text"], 300, 1200, false), Href: u.String()}
}

// stable is JSON with object keys sorted, so equal requests hash alike.
func stable(value any) string {
	switch v := value.(type) {
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = stable(item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, key := range keys {
			parts[i] = string(encoded(key)) + ":" + stable(v[key])
		}
		return "{" + strings.Join(parts, ",") + "}"
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return string(v)
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	default:
		return string(encoded(v))
	}
}

func digest(value any) string {
	sum := sha256.Sum256([]byte(stable(value)))
	return hex.EncodeToString(sum[:])
}

// encoded is the JSON text JavaScript's JSON.stringify writes for value, without HTML escapes.
func encoded(value any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}
