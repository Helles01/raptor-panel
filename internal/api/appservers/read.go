package appservers

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
)

// List is GET /servers, with the filters, sorts and includes of the Application API.
func (h *Handlers) List(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	q, err := listquery.Parse(c.QueryParams(), listquery.Servers)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParams(c.QueryParams())
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Servers)
	list, total, err := h.d.Store.List(c.Request().Context(), q, page, perPage, inc.Load(v, listquery.Servers))
	if err != nil {
		return appkit.StoreError(err)
	}
	items := make([]resp.Item, len(list))
	for i, s := range list {
		items[i] = v.Server(s, inc)
	}
	return appkit.List(c, items, total, perPage, page)
}

// View is GET /servers/{server}.
func (h *Handlers) View(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	id, err := appkit.ID(c, "server")
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Servers)
	s, err := h.d.Store.Find(c.Request().Context(), id, inc.Load(v, listquery.Servers))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.Server(s, inc))
}

// External is GET /servers/external/{external}: the server an outside system calls by its own identifier.
func (h *Handlers) External(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	v := appkit.NewView(p)
	inc := appkit.Wanted(c.QueryParams(), listquery.Servers)
	s, err := h.d.Store.FindExternal(c.Request().Context(), c.Param("external"), inc.Load(v, listquery.Servers))
	if err != nil {
		return appkit.StoreError(err)
	}
	return c.JSON(http.StatusOK, v.Server(s, inc))
}
