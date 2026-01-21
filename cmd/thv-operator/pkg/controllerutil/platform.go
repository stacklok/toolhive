// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/k8s"
)

// PlatformDetectorInterface provides platform detection capabilities
type PlatformDetectorInterface interface {
	DetectPlatform(ctx context.Context) (kubernetes.Platform, error)
}

// SharedPlatformDetector provides shared platform detection across controllers
type SharedPlatformDetector struct {
	detector         kubernetes.PlatformDetector
	detectedPlatform kubernetes.Platform
	once             sync.Once
	config           *rest.Config // Optional config for testing
}

// NewSharedPlatformDetector creates a new shared platform detector
func NewSharedPlatformDetector() *SharedPlatformDetector {
	return &SharedPlatformDetector{
		detector: kubernetes.NewDefaultPlatformDetector(),
	}
}

// NewSharedPlatformDetectorWithDetector creates a new shared platform detector with a custom detector (for testing)
func NewSharedPlatformDetectorWithDetector(detector kubernetes.PlatformDetector) *SharedPlatformDetector {
	return &SharedPlatformDetector{
		detector: detector,
		config:   &rest.Config{}, // Provide a dummy config for testing
	}
}

// DetectPlatform detects the platform once and caches the result
func (s *SharedPlatformDetector) DetectPlatform(ctx context.Context) (kubernetes.Platform, error) {
	var err error
	s.once.Do(func() {
		var cfg *rest.Config
		if s.config != nil {
			cfg = s.config
		} else {
			var configErr error
			cfg, configErr = k8s.GetConfig()
			if configErr != nil {
				err = fmt.Errorf("failed to get kubernetes config for platform detection: %w", configErr)
				return
			}
		}

		s.detectedPlatform, err = s.detector.DetectPlatform(cfg)
		if err != nil {
			err = fmt.Errorf("failed to detect platform: %w", err)
			return
		}

		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Platform detected", "platform", s.detectedPlatform.String())
	})

	return s.detectedPlatform, err
}
