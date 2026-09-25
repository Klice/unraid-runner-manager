package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Klice/unraid-runner-manager/internal/auth"
	"github.com/Klice/unraid-runner-manager/internal/names"
	"github.com/Klice/unraid-runner-manager/internal/store"
)

type userRow struct {
	store.User
	Runners int
	IsSelf  bool
}

type usersData struct {
	Users        []userRow
	Username     string
	Role         string
	TempPassword string
	Error        string
}

func (s *Server) usersData(r *http.Request) (usersData, error) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		return usersData{}, err
	}
	runners, err := s.runners.List(r.Context())
	if err != nil {
		return usersData{}, err
	}
	counts := map[string]int{}
	for _, rn := range runners {
		counts[rn.Owner]++
	}
	me := currentUser(r)
	data := usersData{Role: store.RoleUser}
	for _, u := range users {
		data.Users = append(data.Users, userRow{User: u, Runners: counts[u.Username], IsSelf: u.ID == me.ID})
	}
	return data, nil
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.usersData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.TempPassword = generateTempPassword()
	s.render(w, r, "users", "", data)
}

func generateTempPassword() string {
	name := names.New(nil).Generate()
	suffix, err := auth.RandomToken(4)
	if err != nil {
		suffix = "0000"
	}
	return name + "-" + strings.ToLower(suffix)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	role := r.FormValue("role")
	password := strings.TrimSpace(r.FormValue("password"))
	respond := func(msg string) {
		data, err := s.usersData(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data.Username, data.Role, data.TempPassword, data.Error = username, role, password, msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "users", "", data)
	}
	if !store.ValidUsername(username) {
		respond(store.ErrInvalidUsername.Error())
		return
	}
	if msg := passwordProblem(password); msg != "" {
		respond("Temporary password is too weak. " + msg)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), username, hash, role, true)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) || errors.Is(err, store.ErrInvalidRole) {
			respond(err.Error())
			return
		}
		s.fail(w, r, err)
		return
	}
	s.log.Info("user created", "username", u.Username, "role", u.Role, "by", currentUser(r).Username)
	s.setFlash(w, "ok", fmt.Sprintf("Created %s. Temporary password: %s", u.Username, password))
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) userAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	me := currentUser(r)
	action := r.PathValue("action")
	if target.ID == me.ID && action != "reset" {
		s.setFlash(w, "error", "You cannot "+action+" your own account.")
		http.Redirect(w, r, "/users", http.StatusSeeOther)
		return
	}
	switch action {
	case "reset":
		password := generateTempPassword()
		hash, err := auth.HashPassword(password)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if err := s.store.SetPassword(r.Context(), target.ID, hash, true); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.store.DeleteUserSessions(r.Context(), target.ID)
		s.setFlash(w, "ok", fmt.Sprintf("Password for %s reset. Temporary password: %s", target.Username, password))
	case "disable":
		err = s.store.SetDisabled(r.Context(), target.ID, true)
		s.setFlash(w, "ok", target.Username+" disabled and signed out.")
	case "enable":
		err = s.store.SetDisabled(r.Context(), target.ID, false)
		s.setFlash(w, "ok", target.Username+" enabled.")
	case "delete":
		runners, listErr := s.runners.List(r.Context())
		if listErr != nil {
			s.fail(w, r, listErr)
			return
		}
		for _, rn := range runners {
			if rn.Owner == target.Username {
				s.setFlash(w, "error", "Delete the runners owned by "+target.Username+" first.")
				http.Redirect(w, r, "/users", http.StatusSeeOther)
				return
			}
		}
		err = s.store.DeleteUser(r.Context(), target.ID)
		s.setFlash(w, "ok", target.Username+" removed.")
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("user action", "target", target.Username, "action", action, "by", me.Username)
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}
