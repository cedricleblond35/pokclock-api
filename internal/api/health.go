package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type healthHandler struct {
	buildSHA string
}

// handle retourne 200 avec le build SHA. Le check Postgres pourra être ajouté
// si besoin de granularité (liveness vs readiness).
func (h *healthHandler) handle(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status": "ok",
		"build":  h.buildSHA,
	})
}
