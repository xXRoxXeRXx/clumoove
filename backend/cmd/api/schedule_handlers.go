package main

import (
	"log"
	"net/http"

	"backend/internal/auth"
	"backend/internal/db"
)

// handleListSchedules returns all schedules for the authenticated user
func (s *APIServer) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	schedules, err := db.GetSchedulesForUser(s.db, userID)
	if err != nil {
		log.Printf("handleListSchedules: failed to get schedules for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if schedules == nil {
		schedules = []db.Schedule{}
	}

	writeJSON(w, http.StatusOK, schedules)
}

// handleGetSchedule returns a specific schedule if owned by the user
func (s *APIServer) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrScheduleIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	owns, err := db.VerifyScheduleOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleGetSchedule: error verifying ownership: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owns {
		writeError(w, http.StatusNotFound, ErrScheduleNotFound)
		return
	}

	schedule, err := db.GetSchedule(s.db, id)
	if err != nil {
		log.Printf("handleGetSchedule: failed to get schedule %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, schedule)
}

// handleDeleteSchedule deletes a schedule if owned by the user
func (s *APIServer) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrScheduleIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	owns, err := db.VerifyScheduleOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleDeleteSchedule: error verifying ownership: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owns {
		writeError(w, http.StatusNotFound, ErrScheduleNotFound)
		return
	}

	err = db.DeleteSchedule(s.db, id)
	if err != nil {
		log.Printf("handleDeleteSchedule: failed to delete schedule %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
