package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type DependencyCheck func(context.Context) error

type livenessResponse struct {
	Status string `json:"status"`
}

type readinessResponse struct {
	Status   string `json:"status"`
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
}

type versionResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

func NewLivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireGet(w, r) {
			return
		}

		writeJSON(w, http.StatusOK, livenessResponse{Status: "ok"})
	})
}

func NewReadinessHandler(postgresCheck DependencyCheck, redisCheck DependencyCheck) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireGet(w, r) {
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		pgStatus := dependencyStatus(ctx, postgresCheck)
		redisStatus := dependencyStatus(ctx, redisCheck)

		status := "ok"
		code := http.StatusOK
		if pgStatus != "ok" || redisStatus != "ok" {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}

		writeJSON(w, code, readinessResponse{
			Status:   status,
			Postgres: pgStatus,
			Redis:    redisStatus,
		})
	})
}

func NewVersionHandler(service string, version string, commit string, builtAt string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireGet(w, r) {
			return
		}

		writeJSON(w, http.StatusOK, versionResponse{
			Service: service,
			Version: version,
			Commit:  commit,
			BuiltAt: builtAt,
		})
	})
}

func requireGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

func dependencyStatus(ctx context.Context, check DependencyCheck) string {
	if check == nil {
		return "ok"
	}
	if err := check(ctx); err != nil {
		return "error"
	}
	return "ok"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
