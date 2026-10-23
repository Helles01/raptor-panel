package clientapi

import (
	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/resp"
)

type accountAttributes struct {
	ID        uint64 `json:"id"`
	Admin     bool   `json:"admin"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Language  string `json:"language"`
}

// Account is GET /api/client/account: the details of the signed-in user.
func (h *Handlers) Account(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	a, err := h.d.Accounts.ByID(c.Request().Context(), p.User.ID)
	if err != nil {
		return storeError(err)
	}
	return ok(c, resp.NewItem("user", accountAttributes{
		ID: a.ID, Admin: a.RootAdmin, Username: a.Username, Email: a.Email, FirstName: a.FirstName, LastName: a.LastName, Language: a.Language,
	}))
}
