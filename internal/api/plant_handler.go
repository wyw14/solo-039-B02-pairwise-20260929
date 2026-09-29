package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/wyw14/solo-039/internal/service"
)

func (s *Server) transition(w http.ResponseWriter, r *http.Request) {
	var input transitionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	value, err := s.plant.Transition(r.Context(), input.Grade, input.Source)
	if err != nil {
		var failure *service.TransitionFailure
		if errors.As(err, &failure) {
			// Coherent verdict: batch and feed agree on a conclusion, and the
			// response names the exact step the operator retries from.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":        err.Error(),
				"grade":        failure.Grade,
				"source":       failure.Source,
				"failed_stage": failure.FailedStage,
				"rolled_back":  failure.RolledBack,
				"safe_stop":    failure.SafeStop,
				"landed_phase": failure.LandedPhase,
				"campaign":     value,
				"retry_step":   failure.RetryStep,
				"retry_hint":   failure.RetryHint,
			})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) sample(w http.ResponseWriter, r *http.Request) {
	var input sampleRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().Unix()
	}
	count, err := s.plant.AddSample(input.Value, input.At)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "count": count})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"count": count})
}

func (s *Server) calibrate(w http.ResponseWriter, r *http.Request) {
	revision := s.plant.CalibrateAnalyzer()
	writeJSON(w, http.StatusAccepted, map[string]any{"calibration_revision": revision})
}

func (s *Server) routeProduct(w http.ResponseWriter, r *http.Request) {
	var input routeRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.plant.RouteProduct(input.Target); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"target": input.Target})
}

func (s *Server) lease(w http.ResponseWriter, r *http.Request) {
	var input leaseRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	value, err := s.plant.AcquireReflux(input.Owner, input.Amount)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	timeout, err := boolQuery(r, "relief_timeout")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := s.plant.Shutdown(r.Context(), timeout)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "result": result})
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
