// Package web serves what the browser needs besides the JSON API: the page shell that starts the React frontend, the
// frontend's static assets and its translation data.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/validate"
)

//go:embed locales
var embeddedLocales embed.FS

var (
	localePattern    = regexp.MustCompile(`^[a-z]{2}$`)
	namespacePattern = regexp.MustCompile(`^[a-z]{1,64}$`)
)

// Locales is the translation data of the frontend, loaded once at startup from files embedded in the binary.
//
// The request never selects a file: the locales and namespaces that exist are the keys of an in-memory table built
// here, and a request is answered only if both of its values are keys of that table. That is the fix for the reference
// panel's unauthenticated remote code execution through this endpoint (CVE-2025-49132), where the values reached the
// file system.
type Locales struct {
	bundles map[string]map[string]json.RawMessage
}

// LoadLocales reads <root>/<locale>/<namespace>.json from fsys. Every file must hold a JSON object.
func LoadLocales(fsys fs.FS, root string) (*Locales, error) {
	l := &Locales{bundles: map[string]map[string]json.RawMessage{}}

	locales, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}
	for _, loc := range locales {
		if !loc.IsDir() || !localePattern.MatchString(loc.Name()) {
			return nil, fmt.Errorf("locales/%s is not a two letter locale folder", loc.Name())
		}
		files, err := fs.ReadDir(fsys, root+"/"+loc.Name())
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", loc.Name(), err)
		}
		l.bundles[loc.Name()] = map[string]json.RawMessage{}
		for _, f := range files {
			namespace, isJSON := strings.CutSuffix(f.Name(), ".json")
			if f.IsDir() || !isJSON || !namespacePattern.MatchString(namespace) {
				return nil, fmt.Errorf("locales/%s/%s is not a <namespace>.json file with a lower case name", loc.Name(), f.Name())
			}
			raw, err := fs.ReadFile(fsys, root+"/"+loc.Name()+"/"+f.Name())
			if err != nil {
				return nil, fmt.Errorf("read %s/%s: %w", loc.Name(), f.Name(), err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				return nil, fmt.Errorf("locales/%s/%s is not a JSON object: %w", loc.Name(), f.Name(), err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, raw); err != nil {
				return nil, fmt.Errorf("compact %s/%s: %w", loc.Name(), f.Name(), err)
			}
			l.bundles[loc.Name()][namespace] = compact.Bytes()
		}
	}
	if len(l.bundles) == 0 {
		return nil, fmt.Errorf("no locales found in %s", root)
	}
	return l, nil
}

// EmbeddedLocales loads the translations built into the binary.
func EmbeddedLocales() (*Locales, error) { return LoadLocales(embeddedLocales, "locales") }

// Names lists the available "locale/namespace" pairs, sorted.
func (l *Locales) Names() []string {
	var out []string
	for loc, spaces := range l.bundles {
		for ns := range spaces {
			out = append(out, loc+"/"+ns)
		}
	}
	sort.Strings(out)
	return out
}

// Lookup returns the translation data of a namespace, if both the locale and the namespace exist.
func (l *Locales) Lookup(locale, namespace string) (json.RawMessage, bool) {
	data, ok := l.bundles[locale][namespace]
	return data, ok
}

// Handler serves GET /locales/locale.json?locale=en&namespace=activity as {"en":{"activity":{...}}}.
func (l *Locales) Handler() echo.HandlerFunc {
	return func(c echo.Context) error {
		q := c.QueryParams()
		locale, namespace := single(q["locale"]), single(q["namespace"])

		v := validate.New()
		v.Required("locale", locale)
		v.Matches("locale", locale, localePattern)
		v.Required("namespace", namespace)
		v.Matches("namespace", namespace, namespacePattern)
		if err := v.Err(); err != nil {
			return err
		}

		data, ok := l.Lookup(locale, namespace)
		if !ok {
			return apierr.NotFound()
		}

		body, err := json.Marshal(map[string]map[string]json.RawMessage{locale: {namespace: data}})
		if err != nil {
			return apierr.Internal(err)
		}
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`

		h := c.Response().Header()
		// The same cache policy as the reference: an hour, and stale content may be shown for a day while it refreshes.
		h.Set("Cache-Control", "public, max-age=3600, stale-while-revalidate=86400")
		h.Set("ETag", etag)
		if c.Request().Header.Get("If-None-Match") == etag {
			return c.NoContent(http.StatusNotModified)
		}
		return c.JSONBlob(http.StatusOK, body)
	}
}

// single returns the only value of a query parameter, or "" if it is missing or given more than once.
func single(values []string) string {
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
