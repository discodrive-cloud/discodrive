package api

import (
	"discodrive/internal/auth"
	"encoding/json"
	"net/http"
)

func (s *Server) handlePasskeyApprovalPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
		Action   string `json:"action"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request")
		return
	}
	token, err := s.auth.ApprovePasskeyWithPassword(r.Context(), auth.UserID(r.Context()), req.Password, req.Code, req.Action)
	if err != nil {
		writeError(w, 401, "identity confirmation failed")
		return
	}
	writeJSON(w, 200, map[string]string{"approval_token": token})
}
func (s *Server) handlePasskeyApprovalBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request")
		return
	}
	options, token, err := s.auth.BeginPasskeyApproval(r.Context(), auth.UserID(r.Context()), req.Action)
	if err != nil {
		writeError(w, 401, "identity confirmation failed")
		return
	}
	writeJSON(w, 200, map[string]any{"options": json.RawMessage(options), "session_token": token})
}
func (s *Server) handlePasskeyApprovalFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionToken string          `json:"session_token"`
		Assertion    json.RawMessage `json:"assertion"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request")
		return
	}
	token, err := s.auth.FinishPasskeyApproval(r.Context(), auth.UserID(r.Context()), req.SessionToken, req.Assertion)
	if err != nil {
		writeError(w, 401, "identity confirmation failed")
		return
	}
	writeJSON(w, 200, map[string]string{"approval_token": token})
}
