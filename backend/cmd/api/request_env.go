package main

import (
	"net/http"

	"backend/internal/db"
)

// requestEnv bundles the cross-cutting request helpers every feature module
// needs: authentication lookup, client address resolution, audit logging and
// the handler log levels. The gateway owns it and hands one to each module so
// modules neither declare these fields individually nor default them
// themselves.
type requestEnv struct {
	requireUserID func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP      func(*http.Request) string
	writeAudit    func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf          func(*http.Request, string, ...any)
	infof         func(*http.Request, string, ...any)
	warnf         func(*http.Request, string, ...any)
}

// requestEnv returns the gateway's request helpers for feature modules.
func (s *APIServer) requestEnv() requestEnv {
	return requestEnv{
		requireUserID: s.requireUserID,
		clientIP:      s.clientIP,
		writeAudit:    s.writeAudit,
		logf:          s.logf,
		infof:         s.infof,
		warnf:         s.warnf,
	}
}

// withDefaults returns a copy of the environment with every missing helper
// replaced by a safe default. A missing audit writer is never silently
// dropped: the event is reported through logf under the module's name.
func (e requestEnv) withDefaults(module string) requestEnv {
	defaults := &APIServer{}
	if e.requireUserID == nil {
		e.requireUserID = requireAuthenticatedUserID
	}
	if e.clientIP == nil {
		e.clientIP = defaults.clientIP
	}
	if e.logf == nil {
		e.logf = defaults.logf
	}
	if e.infof == nil {
		e.infof = defaults.infof
	}
	if e.warnf == nil {
		e.warnf = defaults.warnf
	}
	if e.writeAudit == nil {
		logf := e.logf
		e.writeAudit = func(r *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			logf(r, "%s module: audit writer not configured, dropped %s", module, action)
		}
	}
	return e
}
