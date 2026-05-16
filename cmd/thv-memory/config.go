// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the ToolHive memory MCP server.
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	providerOllama = "ollama"
)

// Config is the memory server configuration, loaded from memory-server.yaml.
type Config struct {
	Storage  StorageConfig  `yaml:"storage"`
	Vector   VectorConfig   `yaml:"vector"`
	Embedder EmbedderConfig `yaml:"embedder"`
	Server   ServerConfig   `yaml:"server"`
}

// StorageConfig configures the Store backend.
type StorageConfig struct {
	Provider string `yaml:"provider"` // sqlite (default)
	DSN      string `yaml:"dsn"`
}

// VectorConfig configures the VectorStore backend.
type VectorConfig struct {
	Provider string `yaml:"provider"` // sqlite-vec (default) | qdrant | pgvector
	URL      string `yaml:"url"`
}

// EmbedderConfig configures the Embedder backend.
type EmbedderConfig struct {
	Provider string `yaml:"provider"` // ollama (default) | openai
	URL      string `yaml:"url"`
	Model    string `yaml:"model"`
}

// ServerConfig configures the MCP server itself.
type ServerConfig struct {
	Name           string `yaml:"name"`
	Version        string `yaml:"version"`
	Host           string `yaml:"host"`                     // default 0.0.0.0
	Port           int    `yaml:"port"`                     // default 8080
	LifecycleHours int    `yaml:"lifecycle_interval_hours"` // default 24
}

// LoadConfig reads and validates config from path. The path is operator-supplied
// and expected to be a trusted config file location.
func LoadConfig(path string) (*Config, error) {
	// G304: path is an operator-supplied config file, not user input.
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	applyStorageDefaults(&cfg)
	applyEmbedderDefaults(&cfg)
	applyServerDefaults(&cfg)
	return &cfg, nil
}

func applyStorageDefaults(cfg *Config) {
	if cfg.Storage.Provider == "" {
		cfg.Storage.Provider = "sqlite"
	}
	if cfg.Storage.DSN == "" && cfg.Storage.Provider == "sqlite" {
		cfg.Storage.DSN = "/data/memory.db"
	}
	if cfg.Vector.Provider == "" {
		cfg.Vector.Provider = "sqlite-vec"
	}
}

func applyEmbedderDefaults(cfg *Config) {
	if cfg.Embedder.Provider == "" {
		cfg.Embedder.Provider = providerOllama
	}
	if cfg.Embedder.Model == "" {
		cfg.Embedder.Model = "nomic-embed-text"
	}
	if cfg.Embedder.URL == "" && cfg.Embedder.Provider == providerOllama {
		cfg.Embedder.URL = "http://localhost:11434"
	}
}

func applyServerDefaults(cfg *Config) {
	if cfg.Server.Name == "" {
		cfg.Server.Name = "toolhive-memory"
	}
	if cfg.Server.Version == "" {
		cfg.Server.Version = "0.1.0"
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port <= 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.LifecycleHours <= 0 {
		cfg.Server.LifecycleHours = 24
	}
}
