package api

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// metricsHandler exposes the Prometheus default registry. Collectors are
// registered by internal/metrics.Init at startup.
var metricsHandler = promhttp.Handler()
