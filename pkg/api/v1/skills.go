// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stacklok/toolhive-core/httperr"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/skills"
)

// SkillsRoutes defines the routes for skill management.
type SkillsRoutes struct {
	skillService skills.SkillService
}

// SkillsRouter creates a new router for skill management endpoints.
func SkillsRouter(skillService skills.SkillService) http.Handler {
	routes := SkillsRoutes{
		skillService: skillService,
	}

	r := chi.NewRouter()
	r.Get("/", apierrors.ErrorHandler(routes.listSkills))
	r.Post("/", apierrors.ErrorHandler(routes.installSkill))
	r.Delete("/{name}", apierrors.ErrorHandler(routes.uninstallSkill))
	r.Get("/{name}", apierrors.ErrorHandler(routes.getSkillInfo))
	r.Post("/validate", apierrors.ErrorHandler(routes.validateSkill))
	r.Post("/build", apierrors.ErrorHandler(routes.buildSkill))
	r.Post("/push", apierrors.ErrorHandler(routes.pushSkill))

	return r
}

// listSkills returns a list of installed skills.
//
//	@Summary		List all installed skills
//	@Description	Get a list of all installed skills
//	@Tags			skills
//	@Produce		json
//	@Param			scope	query		string	false	"Filter by scope (user or project)"	Enums(user, project)
//	@Success		200		{object}	skillListResponse
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills [get]
func (s *SkillsRoutes) listSkills(w http.ResponseWriter, r *http.Request) error {
	scope := skills.Scope(r.URL.Query().Get("scope"))
	if err := skills.ValidateScope(scope); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	result, err := s.skillService.List(r.Context(), skills.ListOptions{Scope: scope})
	if err != nil {
		return fmt.Errorf("failed to list skills: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(skillListResponse{Skills: result}); err != nil {
		return fmt.Errorf("failed to encode skills list: %w", err)
	}
	return nil
}

// installSkill installs a skill from a remote source.
//
//	@Summary		Install a skill
//	@Description	Install a skill from a remote source
//	@Tags			skills
//	@Accept			json
//	@Produce		json
//	@Param			request	body		installSkillRequest	true	"Install request"
//	@Success		201		{object}	installSkillResponse
//	@Header			201		{string}	Location	"URI of the installed skill resource"
//	@Failure		400		{string}	string		"Bad Request"
//	@Failure		409		{string}	string		"Conflict"
//	@Failure		500		{string}	string		"Internal Server Error"
//	@Router			/api/v1beta/skills [post]
func (s *SkillsRoutes) installSkill(w http.ResponseWriter, r *http.Request) error {
	var req installSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	if req.Name == "" {
		return httperr.WithCode(
			fmt.Errorf("name is required"),
			http.StatusBadRequest,
		)
	}

	result, err := s.skillService.Install(r.Context(), skills.InstallOptions{
		Name:    req.Name,
		Version: req.Version,
		Scope:   req.Scope,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", fmt.Sprintf("/api/v1beta/skills/%s", result.Skill.Metadata.Name))
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(installSkillResponse{Skill: result.Skill}); err != nil {
		return fmt.Errorf("failed to encode install response: %w", err)
	}
	return nil
}

// uninstallSkill removes an installed skill.
//
//	@Summary		Uninstall a skill
//	@Description	Remove an installed skill
//	@Tags			skills
//	@Param			name	path		string	true	"Skill name"
//	@Param			scope	query		string	false	"Scope to uninstall from (user or project)"	Enums(user, project)
//	@Success		204		{string}	string	"No Content"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/{name} [delete]
func (s *SkillsRoutes) uninstallSkill(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")

	if err := skills.ValidateSkillName(name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := skills.Scope(r.URL.Query().Get("scope"))
	if err := skills.ValidateScope(scope); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	if err := s.skillService.Uninstall(r.Context(), skills.UninstallOptions{
		Name:  name,
		Scope: scope,
	}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getSkillInfo returns detailed information about a skill.
//
//	@Summary		Get skill details
//	@Description	Get detailed information about a specific skill
//	@Tags			skills
//	@Produce		json
//	@Param			name	path		string	true	"Skill name"
//	@Param			scope	query		string	false	"Filter by scope (user or project)"	Enums(user, project)
//	@Success		200		{object}	skills.SkillInfo
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/{name} [get]
func (s *SkillsRoutes) getSkillInfo(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")

	if err := skills.ValidateSkillName(name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := skills.Scope(r.URL.Query().Get("scope"))
	if err := skills.ValidateScope(scope); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	info, err := s.skillService.Info(r.Context(), skills.InfoOptions{Name: name, Scope: scope})
	if err != nil {
		return fmt.Errorf("failed to get skill info: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(info); err != nil {
		return fmt.Errorf("failed to encode skill info: %w", err)
	}
	return nil
}

// validateSkill checks whether a skill definition is valid.
//
//	@Summary		Validate a skill
//	@Description	Validate a skill definition
//	@Tags			skills
//	@Accept			json
//	@Produce		json
//	@Param			request	body		validateSkillRequest	true	"Validate request"
//	@Success		200		{object}	skills.ValidationResult
//	@Failure		501		{string}	string	"Not Implemented"
//	@Router			/api/v1beta/skills/validate [post]
func (*SkillsRoutes) validateSkill(_ http.ResponseWriter, _ *http.Request) error {
	return httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}

// buildSkill builds a skill from a local directory into an OCI artifact.
//
//	@Summary		Build a skill
//	@Description	Build a skill from a local directory
//	@Tags			skills
//	@Accept			json
//	@Produce		json
//	@Param			request	body		buildSkillRequest	true	"Build request"
//	@Success		200		{object}	skills.BuildResult
//	@Failure		501		{string}	string	"Not Implemented"
//	@Router			/api/v1beta/skills/build [post]
func (*SkillsRoutes) buildSkill(_ http.ResponseWriter, _ *http.Request) error {
	return httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}

// pushSkill pushes a built skill artifact to a remote registry.
//
//	@Summary		Push a skill
//	@Description	Push a built skill artifact to a remote registry
//	@Tags			skills
//	@Accept			json
//	@Param			request	body	pushSkillRequest	true	"Push request"
//	@Success		204		{string}	string	"No Content"
//	@Failure		501		{string}	string	"Not Implemented"
//	@Router			/api/v1beta/skills/push [post]
func (*SkillsRoutes) pushSkill(_ http.ResponseWriter, _ *http.Request) error {
	return httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}
