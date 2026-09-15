package executor

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func Handler(service *Service, controlProbe, shardProbe func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if controlProbe(ctx) != nil || shardProbe(ctx) != nil {
			http.Error(w, "{\"status\":\"not_ready\"}", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ready\"}\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		m := service.Metrics()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "executor_claims_total %d\nexecutor_retries_total %d\nexecutor_recoveries_total %d\nexecutor_outcomes_total{outcome=\"succeeded\"} %d\nexecutor_outcomes_total{outcome=\"failed\"} %d\nexecutor_loop_success_total %d\nexecutor_in_flight %d\n", m.Claims, m.Retries, m.Recoveries, m.Succeeded, m.Failed, m.Loops, m.InFlight)
	})
	return mux
}
