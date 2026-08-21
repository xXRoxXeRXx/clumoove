package main

import (
	"net/http"

	"backend/internal/db"
)

// handleListSchedules returns all schedules for the authenticated user
func (s *APIServer) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	schedules, err := db.GetSchedulesForUserContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "handleListSchedules: failed to get schedules for user %s: %v\n", userID, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	owns, err := db.VerifyScheduleOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil {
		s.logf(r, "handleGetSchedule: error verifying ownership: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owns {
		writeError(w, http.StatusNotFound, ErrScheduleNotFound)
		return
	}

	schedule, err := db.GetScheduleContext(r.Context(), s.db, id)
	if err != nil {
		s.logf(r, "handleGetSchedule: failed to get schedule %s: %v\n", id, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	owns, err := db.VerifyScheduleOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil {
		s.logf(r, "handleDeleteSchedule: error verifying ownership: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owns {
		writeError(w, http.StatusNotFound, ErrScheduleNotFound)
		return
	}
	schedule, err := db.GetScheduleContext(r.Context(), s.db, id)
	if err != nil {
		s.logf(r, "handleDeleteSchedule: failed to get schedule %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if schedule.TaskType == "backup" {
		// Backup schedules are coupled to their repository lifecycle and must be
		// paused/resumed through the backup API rather than orphaned here.
		writeConflictError(w, ErrBackupInvalidState)
		return
	}

	err = db.DeleteSchedule(s.db, id)
	if err != nil {
		s.logf(r, "handleDeleteSchedule: failed to delete schedule %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
