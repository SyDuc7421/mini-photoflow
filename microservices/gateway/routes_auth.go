package main

import (
	"encoding/json"
	"net/http"

	"photoshare/internal/apierr"
	"photoshare/microservices/pb/authpb"
)

const maxJSONBody = 64 << 10

func (g *gateway) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		c, ok := readCredentials(w, r)
		if !ok {
			return
		}
		ctx, cancel := withTimeout(r.Context())
		defer cancel()
		u, err := g.auth.Register(ctx, c)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": u.Id, "username": u.Username})
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		c, ok := readCredentials(w, r)
		if !ok {
			return
		}
		ctx, cancel := withTimeout(r.Context())
		defer cancel()
		t, err := g.auth.Login(ctx, c)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": t.Token})
	})
}

func readCredentials(w http.ResponseWriter, r *http.Request) (*authpb.Credentials, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" || in.Password == "" {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return nil, false
	}
	return &authpb.Credentials{Username: in.Username, Password: in.Password}, true
}
