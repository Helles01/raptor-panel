package clientapi

import (
	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/api/resp"
)

// The page sizes of the activity lists: the reference shows 25 and allows 100.
const (
	activityPerPage    = 25
	maxActivityPerPage = 100
)

// AccountActivity is GET /api/client/account/activity: the entries about the signed-in user.
func (h *Handlers) AccountActivity(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	return h.activityList(c, p, func(l ActivityList) (ActivityPage, error) {
		return h.d.Activity.ForUser(c.Request().Context(), p.User.ID, l)
	})
}

// ServerActivity is GET /api/client/servers/{server}/activity: the entries about a server, for someone who holds activity.read.
func (h *Handlers) ServerActivity(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	if p.Server == nil {
		return apierr.Forbidden("This action is unauthorized.")
	}
	return h.activityList(c, p, func(l ActivityList) (ActivityPage, error) {
		return h.d.Activity.ForServer(c.Request().Context(), p.Server.ID, h.d.HideAdminActivity, l)
	})
}

// activityList reads the request, asks fetch for the page and shows every entry as the caller may see it.
func (h *Handlers) activityList(c echo.Context, p authz.Principal, fetch func(ActivityList) (ActivityPage, error)) error {
	q := c.QueryParams()
	query, err := listquery.Parse(q, listquery.ClientActivity)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParamsWith(q, activityPerPage, maxActivityPerPage)
	if err != nil {
		return err
	}
	withActors := wanted(query.Includes)["actor"]

	result, err := fetch(ActivityList{Query: query, Page: page, PerPage: perPage, Exclude: activity.HiddenEvents(), WithActors: withActors})
	if err != nil {
		return storeError(err)
	}

	viewer := activity.Viewer{ID: p.User.ID, Admin: p.User.RootAdmin}
	items := make([]resp.Item, len(result.Entries))
	for i, e := range result.Entries {
		items[i] = resp.NewItem("activity_log", activity.Present(e.Stored, viewer, h.holders, h.d.Location))
		if !withActors {
			continue
		}
		if e.Actor == nil {
			items[i] = items[i].With("actor", nullResource())
			continue
		}
		attrs, err := h.person(*e.Actor)
		if err != nil {
			return err
		}
		items[i] = items[i].With("actor", resp.NewItem("user", attrs))
	}
	return ok(c, resp.NewList(items).WithPagination(resp.NewPagination(c.Request().URL, result.Total, perPage, page, len(items))))
}
