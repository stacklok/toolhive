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
	r.Get("/builds", apierrors.ErrorHandler(routes.listBuilds))
	r.Delete("/builds/{tag}", apierrors.ErrorHandler(routes.deleteBuild))
	r.Get("/content", apierrors.ErrorHandler(routes.getSkillContent))

	return r
}

// listSkills returns a list of installed skills.
//
//	@Summary		List all installed skills
//	@Description	Get a list of all installed skills
//	@Tags			skills
//	@Produce		json
//	@Param			scope	query		string	false	"Filter by scope (user or project)"	Enums(user, project)
//	@Param			client	query		string	false	"Filter by client app"
//	@Param			project_root	query	string	false	"Filter by project root path"
//	@Param			group	query		string	false	"Filter by group name"
//	@Success		200		{object}	skillListResponse
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills [get]
func (s *SkillsRoutes) listSkills(w http.ResponseWriter, r *http.Request) error {
	scope := skills.Scope(r.URL.Query().Get("scope"))
	projectRoot := r.URL.Query().Get("project_root")
	client := r.URL.Query().Get("client")
	group := r.URL.Query().Get("group")

	result, err := s.skillService.List(r.Context(), skills.ListOptions{
		Scope:       scope,
		ClientApp:   client,
		ProjectRoot: projectRoot,
		Group:       group,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(skillListResponse{Skills: result})
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
//	@Failure		401		{string}	string		"Unauthorized (registry refused credentials)"
//	@Failure		404		{string}	string		"Not Found (artifact not present in registry)"
//	@Failure		409		{string}	string		"Conflict"
//	@Failure		429		{string}	string		"Too Many Requests (registry rate limit)"
//	@Failure		500		{string}	string		"Internal Server Error"
//	@Failure		502		{string}	string		"Bad Gateway (upstream registry failure)"
//	@Failure		504		{string}	string		"Gateway Timeout (upstream pull timed out)"
//	@Router			/api/v1beta/skills [post]
func (s *SkillsRoutes) installSkill(w http.ResponseWriter, r *http.Request) error {
	var req installSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	result, err := s.skillService.Install(r.Context(), skills.InstallOptions{
		Name:        req.Name,
		Version:     req.Version,
		Scope:       req.Scope,
		ProjectRoot: req.ProjectRoot,
		Clients:     req.Clients,
		Force:       req.Force,
		Group:       req.Group,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", fmt.Sprintf("/api/v1beta/skills/%s", result.Skill.Metadata.Name))
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(installSkillResponse{Skill: result.Skill})
}

// uninstallSkill removes an installed skill.
//
//	@Summary		Uninstall a skill
//	@Description	Remove an installed skill
//	@Tags			skills
//	@Param			name	path		string	true	"Skill name"
//	@Param			scope	query		string	false	"Scope to uninstall from (user or project)"	Enums(user, project)
//	@Param			project_root	query	string	false	"Project root path for project-scoped skills"
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
	projectRoot := r.URL.Query().Get("project_root")

	if err := s.skillService.Uninstall(r.Context(), skills.UninstallOptions{
		Name:        name,
		Scope:       scope,
		ProjectRoot: projectRoot,
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
//	@Param			project_root	query	string	false	"Project root path for project-scoped skills"
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
	projectRoot := r.URL.Query().Get("project_root")

	info, err := s.skillService.Info(r.Context(), skills.InfoOptions{
		Name:        name,
		Scope:       scope,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(info)
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
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/validate [post]
func (s *SkillsRoutes) validateSkill(w http.ResponseWriter, r *http.Request) error {
	var req validateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	result, err := s.skillService.Validate(r.Context(), req.Path)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(result)
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
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/build [post]
func (s *SkillsRoutes) buildSkill(w http.ResponseWriter, r *http.Request) error {
	var req buildSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	result, err := s.skillService.Build(r.Context(), skills.BuildOptions{
		Path: req.Path,
		Tag:  req.Tag,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(result)
}

// pushSkill pushes a built skill artifact to a remote registry.
//
//	@Summary		Push a skill
//	@Description	Push a built skill artifact to a remote registry
//	@Tags			skills
//	@Accept			json
//	@Param			request	body	pushSkillRequest	true	"Push request"
//	@Success		204		{string}	string	"No Content"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		404		{string}	string	"Not Found"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/push [post]
func (s *SkillsRoutes) pushSkill(w http.ResponseWriter, r *http.Request) error {
	var req pushSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid request body: %w", err),
			http.StatusBadRequest,
		)
	}

	if err := s.skillService.Push(r.Context(), skills.PushOptions{
		Reference: req.Reference,
	}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// listBuilds returns a list of locally-built OCI skill artifacts.
//
//	@Summary		List locally-built skill artifacts
//	@Description	Get a list of all locally-built OCI skill artifacts in the local store
//	@Tags			skills
//	@Produce		json
//	@Success		200		{object}	buildListResponse
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/builds [get]
func (s *SkillsRoutes) listBuilds(w http.ResponseWriter, r *http.Request) error {
	builds, err := s.skillService.ListBuilds(r.Context())
	if err != nil {
		return err
	}

	if builds == nil {
		builds = []skills.LocalBuild{}
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(buildListResponse{Builds: builds})
}

// deleteBuild removes a locally-built OCI skill artifact from the local store.
//
//	@Summary		Delete a locally-built skill artifact
//	@Description	Remove a locally-built OCI skill artifact and its blobs from the local store
//	@Tags			skills
//	@Param			tag	path		string	true	"Artifact tag"
//	@Success		204	{string}	string	"No Content"
//	@Failure		404	{string}	string	"Not Found"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Router			/api/v1beta/skills/builds/{tag} [delete]
func (s *SkillsRoutes) deleteBuild(w http.ResponseWriter, r *http.Request) error {
	tag := chi.URLParam(r, "tag")
	if err := s.skillService.DeleteBuild(r.Context(), tag); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getSkillContent retrieves the SKILL.md body and file listing from an OCI artifact.
//
//	@Summary		Get skill content
//	@Description	Retrieve the SKILL.md body and file listing from an artifact
//	@Description	without installing it. Accepts OCI refs, git refs, or local tags.
//	@Tags			skills
//	@Produce		json
//	@Param			ref	query		string	true	"OCI reference or local build tag"
//	@Success		200	{object}	skills.SkillContent
//	@Failure		400	{string}	string	"Bad Request"
//	@Failure		401	{string}	string	"Unauthorized (registry refused credentials)"
//	@Failure		404	{string}	string	"Not Found (artifact not present in registry)"
//	@Failure		429	{string}	string	"Too Many Requests (registry rate limit)"
//	@Failure		500	{string}	string	"Internal Server Error"
//	@Failure		502	{string}	string	"Bad Gateway (upstream registry or git resolver failure)"
//	@Failure		504	{string}	string	"Gateway Timeout (upstream pull timed out)"
//	@Router			/api/v1beta/skills/content [get]
func (s *SkillsRoutes) getSkillContent(w http.ResponseWriter, r *http.Request) error {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		return httperr.WithCode(
			fmt.Errorf("ref query parameter is required"),
			http.StatusBadRequest,
		)
	}

	content, err := s.skillService.GetContent(r.Context(), skills.ContentOptions{
		Reference: ref,
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(content)
}
