// Package appkit is what the endpoint packages of the Application API (users, locations, nodes, allocations, nests and eggs) share: the
// records their stores return, the way each of them is shown (the reference's transformers), the includes a request may attach, and
// a few helpers to read a route. Nothing here talks to a store.
package appkit

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/fielderr"
)

// ErrNotFound is what a store answers when the record is not there.
var ErrNotFound = errors.New("appkit: not found")

// TakenError is what a store answers when a unique key refuses a value: the field is the API's name for it.
type TakenError struct{ Field string }

func (e *TakenError) Error() string { return "appkit: " + e.Field + " taken" }

// Taken is the 422 for a value that another record has, in the framework's wording.
func Taken(err error) error {
	var t *TakenError
	if !errors.As(err, &t) {
		return nil
	}
	v := validate.New()
	v.Custom(t.Field, "unique", fmt.Sprintf("The %s has already been taken.", strings.ReplaceAll(t.Field, "_", " ")))
	return v.Err()
}

// atom is the form of a time in the Application API: always UTC, as the reference's transformers write it.
const atom = "2006-01-02T15:04:05-07:00"

// Time writes a time as the Application API does.
func Time(t time.Time) string { return t.UTC().Format(atom) }

// rawTime is how a model that is shown as it is writes a time (Laravel's toJSON).
const rawTime = "2006-01-02T15:04:05.000000Z"

// Caller returns the principal of an application route. Without one, or without a key, the handler was reached without the authorizer,
// and it refuses.
func Caller(c echo.Context) (authz.Principal, error) {
	p, ok := authz.From(c)
	if !ok || p.Key == nil {
		return authz.Principal{}, apierr.Forbidden("This action is unauthorized.")
	}
	return p, nil
}

// ID reads a number of the route. What is not a number is a record that is not there.
func ID(c echo.Context, name string) (uint64, error) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		return 0, apierr.NotFound()
	}
	return id, nil
}

// StoreError turns what a store answered into the API's answer: a record that is not there is a 404, a value that is taken a 422.
func StoreError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apierr.NotFound()
	}
	if e := Taken(err); e != nil {
		return e
	}
	return apierr.Internal(err)
}

// ResourceURL is the address of a record, which the answer to a create carries in meta.resource. base is the panel's address.
func ResourceURL(base, path string) string { return strings.TrimRight(base, "/") + path }

// List answers a page of items.
func List(c echo.Context, items []resp.Item, total, perPage, page int) error {
	return c.JSON(http.StatusOK, resp.NewList(items).WithPagination(resp.NewPagination(c.Request().URL, total, perPage, page, len(items))))
}

// Created answers 201 with the item and the address it can be read from.
func Created(c echo.Context, item resp.Item, base, path string) error {
	return c.JSON(http.StatusCreated, item.WithMeta("resource", ResourceURL(base, path)))
}

// View shows records to the holder of one key: what a key may read decides which includes are filled and which are left as null.
type View struct {
	key *authz.Key
}

// NewView returns the view of the key of a principal.
func NewView(p authz.Principal) View { return View{key: p.Key} }

// CanRead reports whether the key may read a resource.
func (v View) CanRead(resource string) bool {
	return v.key != nil && v.key.Access[resource].Allows(auth.AccessRead)
}

// FieldError turns the refusal of a rule package, which names a field, into the 422 for it. What is not such a refusal is an
// unexpected failure.
func FieldError(err error) error {
	if _, ok := fielderr.Of(err); ok {
		return validate.FromField(err)
	}
	return apierr.Internal(err)
}
