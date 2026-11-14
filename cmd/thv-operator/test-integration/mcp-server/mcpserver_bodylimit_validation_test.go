// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var _ = Describe("Schema validation for MCPServer request body limits",
	Label("k8s", "validation"), func() {
		It("accepts an omitted value", func() {
			server := newMinimalMCPServer("mcp-body-limit-omitted", nil)
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		It("accepts a positive value", func() {
			server := newMinimalMCPServer("mcp-body-limit-positive", nil)
			server.Spec.MaxRequestBodySize = 16 << 20
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
		})

		It("rejects a negative value", func() {
			server := newMinimalMCPServer("mcp-body-limit-negative", nil)
			server.Spec.MaxRequestBodySize = -1

			err := k8sClient.Create(ctx, server)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("spec.maxRequestBodySize"))
		})
	})
