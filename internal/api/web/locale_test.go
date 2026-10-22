package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

func testEcho(l *Locales) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		var ae *apierr.Error
		if !errors.As(err, &ae) {
			ae = apierr.Internal(err)
		}
		apierr.Write(c.Response(), ae)
	}
	e.GET("/locales/locale.json", l.Handler())
	return e
}

func get(e *echo.Echo, rawQuery string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/locales/locale.json", nil)
	r.URL.RawQuery = rawQuery
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func embedded(t *testing.T) *Locales {
	t.Helper()
	l, err := EmbeddedLocales()
	if err != nil {
		t.Fatalf("EmbeddedLocales: %v", err)
	}
	return l
}

func TestServesTheEmbeddedActivityTranslations(t *testing.T) {
	e := testEcho(embedded(t))

	rec := get(e, "locale=en&namespace=activity&hash=1a2b3c")

	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var body map[string]map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the expected shape: %v\n%s", err, rec.Body)
	}
	auth := body["en"]["activity"]["auth"].(map[string]any)
	if auth["fail"] != "Failed log in" {
		t.Errorf("auth.fail = %v", auth["fail"])
	}
	// Laravel style placeholders were rewritten to the form the frontend understands.
	if auth["ip-blocked"] != "Blocked request from unlisted IP address for {{identifier}}" {
		t.Errorf("auth.ip-blocked = %v", auth["ip-blocked"])
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=3600, stale-while-revalidate=86400" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

func TestTheDefaultNamespaceIsAnEmptyObject(t *testing.T) {
	rec := get(testEcho(embedded(t)), "locale=en&namespace=translation")

	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"en":{"translation":{}}}` {
		t.Errorf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestConditionalRequests(t *testing.T) {
	e := testEcho(embedded(t))
	first := get(e, "locale=en&namespace=activity")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	same := get(e, "locale=en&namespace=activity", "If-None-Match", etag)
	if same.Code != http.StatusNotModified || same.Body.Len() != 0 {
		t.Errorf("matching ETag: status %d with %d body bytes", same.Code, same.Body.Len())
	}
	other := get(e, "locale=en&namespace=activity", "If-None-Match", `"something-else"`)
	if other.Code != 200 {
		t.Errorf("different ETag: status %d", other.Code)
	}
}

// The request values must never be used as anything but keys of the in-memory table.
func TestHostileValuesAreRejected(t *testing.T) {
	e := testEcho(embedded(t))
	enc := url.QueryEscape

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"path traversal in the locale", "locale=" + enc("../../etc") + "&namespace=activity", 422},
		{"path traversal in the namespace", "locale=en&namespace=" + enc("../../../etc/passwd"), 422},
		{"encoded traversal", "locale=en&namespace=%2e%2e%2f%2e%2e%2fpasswd", 422},
		{"double encoded traversal", "locale=en&namespace=%252e%252e%252f", 422},
		{"backslash", "locale=en&namespace=" + enc(`..\..\windows`), 422},
		{"slash in the namespace", "locale=en&namespace=" + enc("dashboard/account"), 422},
		{"NUL byte", "locale=en&namespace=activity%00.php", 422},
		{"NUL byte in the locale", "locale=en%00&namespace=activity", 422},
		{"upper case", "locale=EN&namespace=activity", 422},
		{"upper case namespace", "locale=en&namespace=Activity", 422},
		{"digits", "locale=e1&namespace=activity", 422},
		{"three letter locale", "locale=eng&namespace=activity", 422},
		{"too long namespace", "locale=en&namespace=" + strings.Repeat("a", 65), 422},
		{"empty locale", "locale=&namespace=activity", 422},
		{"missing locale", "namespace=activity", 422},
		{"missing namespace", "locale=en", 422},
		{"nothing at all", "", 422},
		{"array style parameter", "locale[]=en&namespace=activity", 422},
		{"locale given twice", "locale=en&locale=" + enc("../x") + "&namespace=activity", 422},
		{"namespace given twice", "locale=en&namespace=activity&namespace=strings", 422},
		{"leading space", "locale=" + enc(" en") + "&namespace=activity", 422},
		{"trailing newline", "locale=" + enc("en\n") + "&namespace=activity", 422},
		{"well formed but unknown locale", "locale=hu&namespace=activity", 404},
		{"well formed but unknown namespace", "locale=en&namespace=strings", 404},
		{"a namespace that exists as a PHP file in the reference", "locale=en&namespace=auth", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(e, tt.query)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "root:") || strings.Contains(rec.Body.String(), "<?php") {
				t.Errorf("file content in the response: %s", rec.Body)
			}
		})
	}
}

func TestLoadLocalesValidatesTheTree(t *testing.T) {
	file := func(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr string
	}{
		{"valid", fstest.MapFS{"l/en/a.json": file(`{"k":"v"}`), "l/de/b.json": file(`{}`)}, ""},
		{"locale folder with upper case", fstest.MapFS{"l/EN/a.json": file(`{}`)}, "locale folder"},
		{"file that is not json", fstest.MapFS{"l/en/a.txt": file(`{}`)}, "<namespace>.json"},
		{"namespace with upper case", fstest.MapFS{"l/en/Aa.json": file(`{}`)}, "lower case"},
		{"nested folder", fstest.MapFS{"l/en/sub/a.json": file(`{}`)}, "<namespace>.json"},
		{"not an object", fstest.MapFS{"l/en/a.json": file(`["x"]`)}, "not a JSON object"},
		{"broken json", fstest.MapFS{"l/en/a.json": file(`{`)}, "not a JSON object"},
		{"a loose file next to the locales", fstest.MapFS{"l/readme.txt": file(`x`)}, "locale folder"},
		{"nothing", fstest.MapFS{"l/.keep": file(``)}, "locale folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := LoadLocales(tt.files, "l")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadLocales: %v", err)
				}
				if got := strings.Join(l.Names(), ","); got != "de/b,en/a" {
					t.Errorf("Names = %s", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestEmbeddedLocalesContainWhatTheFrontendNeeds(t *testing.T) {
	names := strings.Join(embedded(t).Names(), ",")
	if names != "en/activity,en/translation" {
		t.Errorf("embedded locales = %s", names)
	}
}
