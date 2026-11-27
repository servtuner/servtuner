// Package prometheustest is a fake Prometheus HTTP API for tests of everything
// that queries Prometheus ("Tests share fixtures"). Serve
// its Handler with tlstest.NewMTLSServer or tlstest.NewPlatformServer to test a
// client in each transport mode. It is only for tests.
package prometheustest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// Target is one active scrape target as /api/v1/targets reports it.
type Target struct {
	Pool     string
	URL      string
	Job      string
	Interval string
	Timeout  string
}

// Sample is one point of a series.
type Sample struct {
	UnixSeconds float64
	Value       string
}

// Series is one result series.
type Series struct {
	Metric  map[string]string
	Samples []Sample
}

// Request is one API call the fake received.
type Request struct {
	Path  string
	Query string
	Start string
	End   string
	Step  string
}

// Server is the fake. Set the fields before serving; they are read, not
// written, by the handler.
type Server struct {
	// Targets answers /api/v1/targets.
	Targets []Target
	// Answer returns the series for a query. Nil means no series.
	Answer func(query string) []Series
	// ErrorFor returns an HTTP status and an error message for a query that
	// should fail, or 0 to let it through.
	ErrorFor func(query string) (status int, message string)

	mutex        sync.Mutex
	requests     []Request
	failuresLeft []int
}

// FailNext makes the next API requests answer with these statuses in order,
// before the server behaves normally again.
func (s *Server) FailNext(statuses ...int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.failuresLeft = append(s.failuresLeft, statuses...)
}

// Requests returns the calls received so far.
func (s *Server) Requests() []Request {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]Request(nil), s.requests...)
}

// Handler serves /api/v1/targets, /api/v1/query_range and /api/v1/query.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/targets", s.serveTargets)
	mux.HandleFunc("/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) { s.serveQuery(w, r, "matrix") })
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) { s.serveQuery(w, r, "vector") })
	return mux
}

// begin records the request and returns a status to fail with, or 0.
func (s *Server) begin(r *http.Request) int {
	_ = r.ParseForm()
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.requests = append(s.requests, Request{
		Path: r.URL.Path, Query: r.Form.Get("query"), Start: r.Form.Get("start"), End: r.Form.Get("end"), Step: r.Form.Get("step"),
	})
	if len(s.failuresLeft) == 0 {
		return 0
	}
	status := s.failuresLeft[0]
	s.failuresLeft = s.failuresLeft[1:]
	return status
}

func (s *Server) serveTargets(w http.ResponseWriter, r *http.Request) {
	if status := s.begin(r); status != 0 {
		http.Error(w, "injected failure", status)
		return
	}
	active := make([]map[string]any, 0, len(s.Targets))
	for _, target := range s.Targets {
		active = append(active, map[string]any{
			"scrapePool":     target.Pool,
			"scrapeUrl":      target.URL,
			"labels":         map[string]string{"job": target.Job},
			"scrapeInterval": target.Interval,
			"scrapeTimeout":  target.Timeout,
			"health":         "up",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{"activeTargets": active, "droppedTargets": []any{}}})
}

func (s *Server) serveQuery(w http.ResponseWriter, r *http.Request, resultType string) {
	if status := s.begin(r); status != 0 {
		http.Error(w, "injected failure", status)
		return
	}
	query := r.Form.Get("query")
	if s.ErrorFor != nil {
		if status, message := s.ErrorFor(query); status != 0 {
			writeJSON(w, status, map[string]any{"status": "error", "errorType": "bad_data", "error": message})
			return
		}
	}
	var series []Series
	if s.Answer != nil {
		series = s.Answer(query)
	}
	result := make([]map[string]any, 0, len(series))
	for _, one := range series {
		entry := map[string]any{"metric": one.Metric}
		if resultType == "matrix" {
			values := make([][]any, 0, len(one.Samples))
			for _, sample := range one.Samples {
				values = append(values, []any{sample.UnixSeconds, sample.Value})
			}
			entry["values"] = values
		} else if len(one.Samples) > 0 {
			entry["value"] = []any{one.Samples[0].UnixSeconds, one.Samples[0].Value}
		}
		result = append(result, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{"resultType": resultType, "result": result}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(fmt.Sprintf("encode fake prometheus response: %v", err))
	}
}

// OneSeries is a convenience answer: a single series with one sample.
func OneSeries(metric map[string]string, unixSeconds float64, value string) []Series {
	return []Series{{Metric: metric, Samples: []Sample{{UnixSeconds: unixSeconds, Value: value}}}}
}
