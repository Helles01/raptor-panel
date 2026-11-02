package appnodes

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/servers"
)

var locationKey = regexp.MustCompile(`^location_ids\[[0-9]*\]$`)

// wholeNumber reads a query value that must be an integer of at least min.
func wholeNumber(v *validate.Validator, q map[string][]string, name string, min int64) int64 {
	vals := q[name]
	if len(vals) == 0 || vals[0] == "" {
		v.Custom(name, "required", fmt.Sprintf("The %s field is required.", name))
		return 0
	}
	n, err := strconv.ParseInt(vals[0], 10, 64)
	switch {
	case err != nil:
		v.Custom(name, "integer", fmt.Sprintf("The %s must be an integer.", name))
		return 0
	case n < min:
		v.Custom(name, "min", fmt.Sprintf("The %s must be at least %d.", name, min))
		return 0
	}
	return n
}

// locations reads location_ids[] (or location_ids[0], location_ids[1]...), which are whole numbers.
func locations(v *validate.Validator, q map[string][]string) []uint64 {
	if _, plain := q["location_ids"]; plain {
		v.Custom("location_ids", "array", "The location ids must be an array.")
		return nil
	}
	var out []uint64
	i := 0
	for key, vals := range q {
		if !locationKey.MatchString(key) {
			continue
		}
		for _, raw := range vals {
			n, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				v.Custom("location_ids", "integer", fmt.Sprintf("The location_ids.%d must be an integer.", i))
				return nil
			}
			out = append(out, n)
			i++
		}
	}
	return out
}

// Deployable is GET /nodes/deployable?memory=&disk=&location_ids[]=: the public nodes that have room for a server of that size, in the
// locations asked for (all if none). With page, a page of them; without, all of them.
func (h *Handlers) Deployable(c echo.Context) error {
	p, err := appkit.Caller(c)
	if err != nil {
		return err
	}
	q := c.QueryParams()
	v := validate.New()
	memory := wholeNumber(v, q, "memory", 0)
	disk := wholeNumber(v, q, "disk", 0)
	locs := locations(v, q)
	if _, paged := q["page"]; paged {
		if _, _, err := resp.PageParams(q); err != nil {
			return err
		}
	}
	if err := v.Err(); err != nil {
		return err
	}

	cands, err := h.d.Store.Candidates(c.Request().Context())
	if err != nil {
		return appkit.StoreError(err)
	}
	usage := make([]servers.NodeUsage, len(cands))
	byID := make(map[uint64]appkit.Node, len(cands))
	for i, cand := range cands {
		usage[i] = cand.Usage
		byID[cand.Usage.ID] = cand.Node
	}
	viable, err := servers.FindViableNodes(usage, locs, memory, disk)
	if err != nil {
		return apierr.Display(err.Error())
	}
	sort.Slice(viable, func(i, j int) bool { return viable[i].ID < viable[j].ID })

	view := appkit.NewView(p)
	if _, paged := q["page"]; !paged {
		items := make([]resp.Item, len(viable))
		for i, n := range viable {
			items[i] = view.Node(byID[n.ID], nil)
		}
		return c.JSON(http.StatusOK, resp.NewList(items))
	}
	page, perPage, _ := resp.PageParams(q)
	total := len(viable)
	from := min((page-1)*perPage, total)
	to := min(from+perPage, total)
	items := make([]resp.Item, 0, to-from)
	for _, n := range viable[from:to] {
		items = append(items, view.Node(byID[n.ID], nil))
	}
	return appkit.List(c, items, total, perPage, page)
}
