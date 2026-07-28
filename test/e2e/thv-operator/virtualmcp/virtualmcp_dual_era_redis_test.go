// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	coremcp "github.com/stacklok/toolhive/pkg/mcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// dualEraRedisKeyPrefix is the vMCP session-storage key prefix used by this
// spec. It must end in ':' (pkg/transport/session/session_data_storage_redis.go
// requires it to avoid key collisions across tenants), and is shared as a
// package-level constant because the concurrency and Redis-outage specs that
// build on this one (added in later steps of this file's plan) key their own
// direct Redis reads/writes off the exact same prefix.
const dualEraRedisKeyPrefix = "thv:vmcp:dualera:"

// This spec is the Redis-backed counterpart of virtualmcp_dual_era_backends_test.go:
// same mixed Legacy+Modern backend set behind one vMCP, but with session storage
// backed by Redis instead of the in-memory default. It exists as the shared
// fixture for two follow-on specs (not yet added): concurrent Legacy+Modern
// traffic against the Redis-backed session store, and a Redis-outage spec that
// scales Redis to 0 replicas mid-test.
//
// This file is also the end-to-end assertion that Redis session storage does
// NOT close vMCP's Modern capability gate (modernDispatchBlockers,
// pkg/vmcp/server/modern_gate.go): Legacy clients keep their shared,
// reconstructible sessions while Modern clients are served statelessly and
// store nothing. Redis therefore must never be added to the gate's blocker
// list — the Modern legs below fail with -32022 if it is.
//
// Both backends run the SAME yardstick-server image and set BACKEND_MODE=echo
// explicitly so that STATELESS is the ONLY difference between them. (TRANSPORT
// is not set here at all: CreateMultipleMCPServersInParallel (helpers.go
// ~:538-541, ~:562-564) already defaults Transport to "streamable-http" and
// prepends a TRANSPORT env entry itself -- see the Legacy backend's Env
// comment below.) Setting BACKEND_MODE explicitly is deliberate: since #6004
// yardstick-server is a single go-sdk v1.7 build serving both MCP eras, and a
// forgotten STATELESS would silently collapse this "mixed" backend set into
// two Legacy backends -- every assertion in this file would still pass while
// testing one era twice.
// Using two yardsticks (rather than the gofetch+yardstick pairing in
// virtualmcp_dual_era_backends_test.go) is also deliberate: both eras must
// expose the SAME echo tool so a value can be round-tripped on either client
// edge, which the concurrency spec (Step 3) needs to prove no cross-delivery
// between principals occurs.
//
// IMPORTANT -- this spec must NOT use CreateInitializedMCPClient, WaitForExpectedTools,
// or ToolsContainAll (all in helpers.go / wait_for_tools_helpers.go). Those build a
// go-sdk/mcpcompat client, and go-sdk v1.7's Connect is Modern-first: it sends
// server/discover before anything else. This vMCP serves Modern (Redis session
// storage does not close the capability gate -- see the header above), so
// classifyingHandler routes server/discover to dispatchModern instead of the
// SDK's stateful fallback (pkg/vmcp/server/classification.go), and such a client
// always negotiates Modern and gets no session -- there is no way to make it open
// a Legacy session against this vMCP. The observed failure is mcpcompat's
// Initialize unconditionally installing list-changed handlers, which makes go-sdk
// open a "subscriptions/listen" stream vMCP does not implement, answered with
// HTTP 404 and surfaced as go-sdk's ErrSessionMissing ("session not found")
// despite Initialize having "succeeded". Use *e2e.RawMCPClient instead: it pins
// the era explicitly per request (Legacy via e2e.NewLegacyRequest + the
// MCP-Protocol-Version header), which is what actually gets a Legacy session
// from a Modern-serving vMCP.
var _ = Describe("VirtualMCPServer Dual-Era Backends over Redis Session Storage", Ordered, func() {
	var (
		timeout         = 5 * time.Minute
		pollingInterval = 2 * time.Second

		redisName      string
		mcpGroupName   string
		legacyBackend  string
		modernBackend  string
		vmcpServerName string
		vmcpNodePort   int32
		rawClient      *e2e.RawMCPClient
	)

	// assertRevisions checks that got[legacyBackend] and got[modernBackend] hold
	// the expected MCP revision, returning a descriptive error (listing the whole
	// map) otherwise. Mirrors assertRevisions in virtualmcp_dual_era_backends_test.go.
	assertRevisions := func(got map[string]string) error {
		if rev := got[legacyBackend]; rev != coremcp.MCPVersionLegacy {
			return fmt.Errorf("legacy backend %s: want revision %q, got %q (all: %v)",
				legacyBackend, coremcp.MCPVersionLegacy, rev, got)
		}
		if rev := got[modernBackend]; rev != coremcp.MCPVersionModern {
			return fmt.Errorf("modern backend %s: want revision %q, got %q (all: %v)",
				modernBackend, coremcp.MCPVersionModern, rev, got)
		}
		return nil
	}

	BeforeAll(func() {
		ts := time.Now().UnixNano()
		redisName = fmt.Sprintf("dualera-redis-%d", ts)
		mcpGroupName = fmt.Sprintf("dualera-group-%d", ts)
		legacyBackend = fmt.Sprintf("dualera-legacy-%d", ts)
		modernBackend = fmt.Sprintf("dualera-modern-%d", ts)
		vmcpServerName = fmt.Sprintf("dualera-vmcp-%d", ts)

		var err error
		rawClient, err = e2e.NewRawMCPClient(20 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		By("Deploying Redis for vMCP session storage")
		deployRedis(redisName)

		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
			"Test MCP Group for VirtualMCP dual-era Redis session E2E tests", timeout, pollingInterval)

		By("Creating Legacy and Modern (stateless) yardstick backends in parallel")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{
				Name:      legacyBackend,
				Namespace: defaultNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				// No STATELESS: this backend negotiates down and vMCP classifies it
				// Legacy from server/discover's supportedVersions. BACKEND_MODE is set
				// explicitly (see the header comment) so STATELESS is the only
				// difference between the two backends below. No TRANSPORT either --
				// CreateMultipleMCPServersInParallel already sets it from
				// BackendConfig.Transport (defaulting to "streamable-http"), so adding
				// it here would produce a duplicate Env entry.
				Env: []mcpv1beta1.EnvVar{
					{Name: "BACKEND_MODE", Value: "echo"},
				},
			},
			{
				Name:      modernBackend,
				Namespace: defaultNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
				Env: []mcpv1beta1.EnvVar{
					{Name: "STATELESS", Value: "true"}, // Modern (2026-07-28) capable backend
					{Name: "BACKEND_MODE", Value: "echo"},
				},
			},
		}, timeout, pollingInterval)

		By("Creating VirtualMCPServer over the mixed backend set with Redis session storage")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Group: mcpGroupName,
				Aggregation: &vmcpconfig.AggregationConfig{
					ConflictResolution: "prefix", // tool names become "<backendName>_echo"
				},
				// Health monitoring is what resolves and caches MCPRevision; it is
				// opt-in (see the header comment), so it must be turned on explicitly
				// for the era-gate check below to ever see a non-empty value.
				Operational: &vmcpconfig.OperationalConfig{
					FailureHandling: &vmcpconfig.FailureHandlingConfig{
						HealthCheckInterval: vmcpconfig.Duration(10 * time.Second),
						// Must be set explicitly and be strictly LESS than the interval.
						// Supplying the failureHandling object at all makes kubebuilder
						// apply its siblings' CRD defaults, and healthCheckTimeout
						// defaults to 10s -- equal to the interval above, which
						// pkg/vmcp/config/validator.go rejects ("must be less than
						// healthCheckInterval to prevent checks from queuing up"). The
						// vmcp pod then crash-loops on config validation and the
						// VirtualMCPServer never leaves Pending, so every assertion here
						// fails on a readiness timeout with no hint of the real cause.
						HealthCheckTimeout:      vmcpconfig.Duration(2 * time.Second),
						UnhealthyThreshold:      3,
						StatusReportingInterval: vmcpconfig.Duration(5 * time.Second),
					},
				},
			}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
			v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
				Provider:  mcpv1beta1.SessionStorageProviderRedis,
				Address:   fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace),
				KeyPrefix: dualEraRedisKeyPrefix,
			}),
			// One replica, for three reasons:
			//  1. Every request takes the local-session Validate branch
			//     (WithSessionIdManager is wired unconditionally, server.go:570),
			//     which is what will make the outage spec's 503 deterministic. At two
			//     replicas, a request landing on the non-owning pod without a cached
			//     rehydration takes the rehydration path instead, whose transient-error
			//     branch returns 404 rather than 503 (session_manager.go:437-443 states
			//     this mapping explicitly).
			//  2. GetVMCPStatus and readRedisSessionBackendIDs (Step 3) each observe a
			//     single pod's view of the world, so the era gate below and Step 3's
			//     direct Redis read are deterministic only when there is exactly one
			//     pod to ask.
			//  3. SessionAffinity becomes moot -- there is only one pod to be sticky
			//     to -- so it is deliberately left unset rather than set to a value
			//     that would do nothing.
			v1beta1test.WithVMCPReplicas(1),
			v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.ServiceType = "NodePort"
			}),
		)
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, defaultNamespace, timeout, pollingInterval)

		// Fail-fast: without this, a dropped/rejected spec.sessionStorage (bad
		// Address, bad KeyPrefix) still leaves the vMCP Ready on its in-memory
		// fallback, and every assertion below is satisfied byte-for-byte by that
		// fallback -- the only symptom would be an unexplained Redis GET miss much
		// later.
		By("Confirming the operator wired Redis session storage without a warning")
		WaitForCondition(ctx, k8sClient, vmcpServerName, defaultNamespace,
			mcpv1beta1.ConditionSessionStorageWarning, "False", timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover both backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, defaultNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, defaultNamespace, timeout, pollingInterval)

		// Era gate: this belongs here, as a BeforeAll precondition, rather than as
		// its own It. As a separate spec it would duplicate
		// virtualmcp_dual_era_backends_test.go:222-253 and trip the Redundancy rule
		// in .claude/rules/testing.md; as a precondition it is the same guard with
		// no duplicate spec (the CLI tier does exactly this at
		// vmcp_dual_era_test.go:108-112). It guards against a forgotten STATELESS
		// silently collapsing the mixed backend set to all-Legacy -- every
		// assertion below would still pass while testing one era twice.
		By("Waiting for vMCP to classify each backend's MCP revision correctly")
		Eventually(func() error {
			status, err := GetVMCPStatus(vmcpNodePort)
			if err != nil {
				return fmt.Errorf("failed to get /status: %w", err)
			}
			got := make(map[string]string, len(status.Backends))
			for _, b := range status.Backends {
				got[b.Name] = b.MCPRevision
			}
			return assertRevisions(got)
		}, timeout, pollingInterval).Should(Succeed())

		By("Waiting for both backends' echo tools to be aggregated")
		// Final warm-up: confirms tool aggregation itself is stable (a concern
		// independent of the revision classification checked above) before any
		// It in this file runs. This CANNOT use WaitForExpectedTools -- see the
		// file header comment -- so it is reimplemented here on the raw client: a
		// fresh Legacy session on every attempt, then a Legacy tools/list. The
		// fresh-session-per-attempt part is the point (not incidental): it is what
		// WaitForExpectedTools does too, and for the same reason -- session-scoped
		// tool discovery silently skips a backend that is not yet ready, so retrying
		// on the SAME session would just re-observe the same stale, incomplete list.
		vmcpURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
		Eventually(func() error {
			sessionID, err := dualEraLegacyInitErr(rawClient, vmcpURL)
			if err != nil {
				return fmt.Errorf("legacy init: %w", err)
			}
			names, err := dualEraLegacyListTools(rawClient, vmcpURL, sessionID)
			if err != nil {
				return fmt.Errorf("legacy tools/list: %w", err)
			}
			got := make(map[string]bool, len(names))
			for _, n := range names {
				got[n] = true
			}
			for _, want := range []string{legacyBackend + "_echo", modernBackend + "_echo"} {
				if !got[want] {
					return fmt.Errorf("missing expected tool %q; got %v", want, names)
				}
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		if err := k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpServerName, defaultNamespace)); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete VirtualMCPServer: %v\n", err)
		}

		By("Cleaning up backend MCPServers")
		for _, name := range []string{legacyBackend, modernBackend} {
			backend := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
			}
			if err := k8sClient.Delete(ctx, backend); err != nil {
				GinkgoWriter.Printf("Warning: failed to delete backend %s: %v\n", name, err)
			}
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
		}
		if err := k8sClient.Delete(ctx, mcpGroup); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete MCPGroup: %v\n", err)
		}

		// Redis is torn down last (vMCP before Redis) so the vmcp pod never
		// outlives its session store.
		By("Cleaning up Redis")
		cleanupRedis(redisName)

		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpServerName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{})
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("round-trips a tools/call on both the Legacy and the Modern client edge", func() {
		vmcpURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

		// Prove both client-facing helpers actually reach a working backend, not
		// just that the tool names are listed (per .claude/rules/testing.md's E2E
		// Test Coverage rule: functional behavior, not just infrastructure state).
		// Tool aggregation itself is already proven by the BeforeAll warm-up --
		// repeating a tools/list here would just be the same assertion twice.
		//
		// Inputs must match yardstick's echo tool's input schema, "^[a-zA-Z0-9]+$"
		// (no hyphens/underscores/dots) -- see the doc comments on dualEraLegacyCall
		// and dualEraModernCall.
		//
		// This single-request-per-era shape is a strict subset of what the
		// concurrency spec below also exercises, and is kept deliberately rather
		// than left as unexplained overlap: it isolates plain Legacy/Modern
		// routing on one request each, so a red concurrency spec (many requests
		// racing) can be told apart from a red routing spec (this one) -- the
		// former points at concurrency handling, the latter at routing itself.
		legacySessionID := dualEraLegacyInit(rawClient, vmcpURL)
		legacyResp := dualEraLegacyCall(rawClient, vmcpURL, legacySessionID, legacyBackend+"_echo", "dualeraredislegacy")
		expectDualEraEcho(legacyResp, "dualeraredislegacy", "")

		modernResp := dualEraModernCall(rawClient, vmcpURL, modernBackend+"_echo", "dualeraredismodern")
		expectDualEraEcho(modernResp, "dualeraredismodern", "complete")
	})

	It("serves concurrent Legacy-session and Modern-stateless traffic over a shared Redis session store, storing session state only for the Legacy backend", func() {
		vmcpURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

		// One Legacy session, established up front and reused by every Legacy
		// request below and by the final survival check at the end.
		sessionID := dualEraLegacyInit(rawClient, vmcpURL)

		const perEra = 4
		const rounds = 3 // covers same-era interleaving too, not just one batch

		// dualEraConcurrentResult pairs a response (and any Send error) with
		// the input THIS goroutine sent, so the post-join assertion can check
		// that this request got back its own echo rather than another
		// goroutine's -- that mismatch is the cross-delivery failure this spec
		// exists to catch. err is asserted before resp is touched (see the
		// post-join loop below): the goroutines below call only the
		// error-returning dualEra*CallErr helpers and never assert on the
		// result themselves, so a nil resp from a failed send must never
		// reach expectDualEraEcho on the main goroutine.
		type dualEraConcurrentResult struct {
			resp   *e2e.RawResponse
			err    error
			input  string
			legacy bool
		}

		for round := 0; round < rounds; round++ {
			total := 2 * perEra
			// Pre-sized slice indexed by goroutine number -- no shared map,
			// no append from goroutines.
			results := make([]dualEraConcurrentResult, total)
			start := make(chan struct{})
			var wg sync.WaitGroup

			// The goroutines below call ONLY dualEraLegacyCallErr /
			// dualEraModernCallErr -- the error-returning variants -- and
			// store (resp, err) in results without asserting on either. A
			// Fail() (what a failing Expect does) inside a goroutine with no
			// GinkgoRecover on that goroutine's stack panics the whole test
			// process, not just this spec, so no Expect/Fail of ours may run
			// here. Every substantive assertion -- the error check first,
			// then expectDualEraEcho, then the session-id header check --
			// happens below, after the join, on the main goroutine.
			for i := 0; i < perEra; i++ {
				idx, input := i, fmt.Sprintf("round%dlegacy%d", round, i)
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					resp, err := dualEraLegacyCallErr(rawClient, vmcpURL, sessionID, legacyBackend+"_echo", input)
					results[idx] = dualEraConcurrentResult{resp: resp, err: err, input: input, legacy: true}
				}()
			}
			for i := 0; i < perEra; i++ {
				idx, input := perEra+i, fmt.Sprintf("round%dmodern%d", round, i)
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					resp, err := dualEraModernCallErr(rawClient, vmcpURL, modernBackend+"_echo", input)
					results[idx] = dualEraConcurrentResult{resp: resp, err: err, input: input, legacy: false}
				}()
			}

			close(start) // release every goroutine together, so requests are genuinely in flight simultaneously

			// Timeout barrier per .claude/rules/testing.md's "Concurrent Tests:
			// Always Add Timeouts to Blocking Barriers" -- a panicking
			// goroutine or a routing regression that drops a response must
			// not hang the whole suite.
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(60 * time.Second):
				Fail(fmt.Sprintf("round %d: timed out waiting for %d concurrent requests", round, total))
			}

			for _, r := range results {
				Expect(r.err).ToNot(HaveOccurred(), "input %q", r.input)
				if r.legacy {
					expectDualEraEcho(r.resp, r.input, "")
					continue
				}
				expectDualEraEcho(r.resp, r.input, "complete")
				Expect(r.resp.Headers.Get(e2e.HeaderMCPSessionID)).To(BeEmpty(),
					"Modern edge must not mint a session")
			}
		}

		By("Confirming Redis stores session state only for the Legacy backend")
		backendIDs, err := readRedisSessionBackendIDs(redisName, dualEraRedisKeyPrefix, sessionID)
		Expect(err).ToNot(HaveOccurred())
		// True regardless of the health monitor's MCPRevision cache state:
		//  - The actual state here is warm: this file's BeforeAll era gate already
		//    forced classification of both backends before any It runs. On the
		//    warm path, initOneBackend's isKnownModern check short-circuits and
		//    returns (nil, true) for the Modern backend without ever attempting a
		//    connection to it (pkg/vmcp/session/factory.go:281-287).
		//    WithRevisionLookup is wired whenever the backend client implements
		//    vmcp.RevisionReporter, which the default client does
		//    (pkg/vmcp/cli/serve.go:361-363).
		//  - And even off that path (a cold cache), populateBackendMetadata only
		//    writes MetadataKeyBackendSessionPrefix+workloadID when the backend's
		//    connection reports a non-empty session ID (factory.go:414-425:
		//    "if sessID := r.conn.SessionID(); sessID != "" { ... }"). A stateless
		//    Modern backend never mints an Mcp-Session-Id, so no
		//    vmcp.backend.session.<modern> key can be written either way.
		// Backend ID is the MCPServer resource name
		// (pkg/vmcp/k8s/backend_reconciler.go:41, pkg/vmcp/registry.go:363-368).
		Expect(backendIDs).To(HaveKey("vmcp.backend.session." + legacyBackend))
		Expect(backendIDs).ToNot(HaveKey("vmcp.backend.session." + modernBackend))

		By("Confirming the Legacy session still works after the direct Redis read above")
		// Rounds 1 and 2 already reused sessionID after round 0's concurrent
		// Modern traffic, so "Modern traffic did not disturb the Legacy session"
		// was already established before this call runs. What this call actually
		// adds: the session still works after the direct Redis GET immediately
		// above, i.e. that read did not consume or invalidate anything, at the
		// very end of the spec.
		finalResp := dualEraLegacyCall(rawClient, vmcpURL, sessionID, legacyBackend+"_echo", "aftersurvive")
		expectDualEraEcho(finalResp, "aftersurvive", "")
	})

	// This It is deliberately last: it destroys Redis's data (scaling it to 0
	// wipes every key, since the fixture has no persistence) and must not run
	// before the two specs above, which depend on Redis being up throughout.
	// It restores Redis itself partway through (see the "Restoring Redis" step
	// below), so AfterAll does not need to -- and must not skip Redis cleanup
	// on the assumption this It always reaches that step.
	It("returns 503 on the Legacy session path during a Redis outage while the Modern path stays unaffected, then recovers with a fresh session", func() {
		vmcpURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)
		vmcpLabels := map[string]string{
			"app.kubernetes.io/name":     "virtualmcpserver",
			"app.kubernetes.io/instance": vmcpServerName,
		}

		By("Recording the vmcp pod's restart count before the outage")
		podsBefore, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpServerName, defaultNamespace)
		Expect(err).ToNot(HaveOccurred())
		Expect(podsBefore.Items).To(HaveLen(1), "replicas=1 fixture: exactly one vmcp pod expected")
		restartCountBefore := vmcpContainerRestartCount(&podsBefore.Items[0])

		// Load-bearing: the 503 asserted below requires a session established
		// BEFORE Redis goes down -- see that assertion's comment.
		By("Establishing a Legacy session and one successful call before the outage")
		sessionID := dualEraLegacyInit(rawClient, vmcpURL)
		preOutageResp := dualEraLegacyCall(rawClient, vmcpURL, sessionID, legacyBackend+"_echo", "beforeoutage")
		expectDualEraEcho(preOutageResp, "beforeoutage", "")

		By("Scaling Redis to 0 replicas to simulate a session-store outage")
		Eventually(func() error { return scaleRedis(redisName, 0) }, timeout, pollingInterval).Should(Succeed())
		Eventually(func() (int, error) {
			podList := &corev1.PodList{}
			if err := k8sClient.List(ctx, podList, client.InNamespace(defaultNamespace),
				client.MatchingLabels{"app": redisName}); err != nil {
				return -1, err
			}
			return len(podList.Items), nil
		}, timeout, pollingInterval).Should(Equal(0), "Redis pod should be gone after scaling to 0")

		By("Confirming the Legacy path 503s on the pre-outage session")
		// With replicas=1 (see the BeforeAll comment on WithVMCPReplicas),
		// every request takes sessionmanager.Manager.Validate's local-session
		// branch, which returns (false, err) on Redis's transient error; the
		// transport maps that to 503 (session_manager.go:444-471,
		// toolhive-core@v0.0.34/mcpcompat/server/transports.go:462-496). A
		// client that instead runs "initialize" DURING the outage gets a 200
		// with NO Mcp-Session-Id -- Manager.Generate returns "" after two
		// failed storage.Create attempts (session_manager.go:238-267), and
		// go-sdk hands out an unusable ephemeral session, never a 503. That
		// quirk is NOT asserted here; this pins the intended contract for a
		// session that predates the outage, not that edge case.
		//
		// Wrapped in Eventually: there is a short window right after the
		// Deployment scales to 0 where the Redis Service may still have stale
		// endpoints. dualEraLegacyCallErr (rather than dualEraLegacyCall) lets
		// a transient Send error be retried instead of aborting the spec,
		// mirroring how dualEraLegacyInitErr exists alongside dualEraLegacyInit.
		var outageResp *e2e.RawResponse
		Eventually(func() (int, error) {
			resp, err := dualEraLegacyCallErr(rawClient, vmcpURL, sessionID, legacyBackend+"_echo", "duringoutage")
			if err != nil {
				return 0, err
			}
			outageResp = resp
			return resp.StatusCode, nil
		}, timeout, pollingInterval).Should(Equal(http.StatusServiceUnavailable))
		// The 503 body is written with http.Error, so it is plain text, not a
		// JSON-RPC envelope -- assert on status + body substring, not resp.Error.
		Expect(string(outageResp.Body)).To(ContainSubstring("session validation unavailable"))

		By("Confirming the Modern path is unaffected while Redis is down")
		// dispatchModern (pkg/vmcp/server/modern_dispatch.go) holds no
		// session-store reference at all, which is why both calls below keep
		// succeeding through the whole outage. The second call is the bridge
		// cell -- vMCP synthesizes a per-request backend session for the
		// Legacy backend reached via the Modern client edge -- and it
		// succeeding during an outage is the sharpest proof that the Modern
		// path touches the session store zero times. Consistently (not one
		// call) so a single lucky success cannot pass.
		Consistently(func() error {
			modernResp := dualEraModernCall(rawClient, vmcpURL, modernBackend+"_echo", "duringoutage")
			if err := dualEraEchoErr(modernResp, "duringoutage", "complete"); err != nil {
				return fmt.Errorf("modern backend: %w", err)
			}
			bridgeResp := dualEraModernCall(rawClient, vmcpURL, legacyBackend+"_echo", "duringoutage")
			if err := dualEraEchoErr(bridgeResp, "duringoutage", "complete"); err != nil {
				return fmt.Errorf("bridge cell (legacy backend via modern edge): %w", err)
			}
			return nil
		}, 5*time.Second, pollingInterval).Should(Succeed())

		By("Confirming the outage did not restart or un-ready the vmcp pod")
		// Liveness /health is an unconditional 200 and readiness /readyz gates
		// only on the watcher cache sync (pkg/vmcp/server/server.go:915,:955),
		// so a Redis outage that starts AFTER the pod is already running must
		// not restart it. This is distinct from Redis being down at pod
		// START, which WOULD fail startup -- NewRedisSessionDataStorage pings
		// Redis on construction -- which is exactly why the ordering in this
		// spec (Redis up when the pod started, taken down mid-run) matters.
		podsAfter, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpServerName, defaultNamespace)
		Expect(err).ToNot(HaveOccurred())
		Expect(podsAfter.Items).To(HaveLen(1))
		Expect(vmcpContainerRestartCount(&podsAfter.Items[0])).To(Equal(restartCountBefore))
		WaitForPodsReady(ctx, k8sClient, defaultNamespace, vmcpLabels, timeout, pollingInterval)

		By("Restoring Redis")
		Eventually(func() error { return scaleRedis(redisName, 1) }, timeout, pollingInterval).Should(Succeed())
		WaitForPodsReady(ctx, k8sClient, defaultNamespace, map[string]string{"app": redisName}, timeout, pollingInterval)

		By("Confirming a fresh Legacy session works after recovery")
		var newSessionID string
		Eventually(func() error {
			id, err := dualEraLegacyInitErr(rawClient, vmcpURL)
			if err != nil {
				return err
			}
			newSessionID = id
			return nil
		}, timeout, pollingInterval).Should(Succeed())
		Expect(newSessionID).ToNot(BeEmpty())
		recoveredResp := dualEraLegacyCall(rawClient, vmcpURL, newSessionID, legacyBackend+"_echo", "afterrecovery")
		expectDualEraEcho(recoveredResp, "afterrecovery", "")

		By("Confirming the stale pre-outage session was cleared by the outage, not merely surviving it")
		// Redis here is a plain in-memory Deployment with no persistence, so
		// scaling it to 0 earlier wiped every key; Validate now sees
		// redis.Nil -> ErrSessionNotFound -> (true, nil) -> 404
		// (session_manager.go:444-471, transports.go:462-496). This is NOT
		// asserting session survival -- it proves the outage really cleared
		// state, so the recovery above is genuine functional recovery, not a
		// session that was never gone.
		var staleResp *e2e.RawResponse
		Eventually(func() (int, error) {
			resp, err := dualEraLegacyCallErr(rawClient, vmcpURL, sessionID, legacyBackend+"_echo", "stalesession")
			if err != nil {
				return 0, err
			}
			staleResp = resp
			return resp.StatusCode, nil
		}, timeout, pollingInterval).Should(Equal(http.StatusNotFound))
		Expect(string(staleResp.Body)).To(ContainSubstring("Session terminated"))
	})
})

// dualEraLegacyInit performs the Legacy (2025-11-25) initialize handshake
// against url on client: an "initialize" request followed by the
// notifications/initialized notification the spec requires a client to send
// immediately after, before any other request on the session. Mirrors
// legacyInitialize in vmcp_dual_era_test.go. client is taken as a parameter
// rather than captured, for consistency with the other dualEra* helpers below
// (this one is only ever called from the main spec goroutine, not from the
// concurrency spec's fan-out).
//
// Aborts the spec via Expect on failure. Use dualEraLegacyInitErr instead
// inside an Eventually retry loop (e.g. the BeforeAll warm-up), where a
// transient failure must be retried rather than failing the spec outright.
func dualEraLegacyInit(mcpClient *e2e.RawMCPClient, url string) string {
	GinkgoHelper()
	sessionID, err := dualEraLegacyInitErr(mcpClient, url)
	Expect(err).ToNot(HaveOccurred())
	return sessionID
}

// dualEraLegacyInitErr is the error-returning variant of dualEraLegacyInit,
// for use inside an Eventually retry loop. It delegates to the shared
// legacySessionInit primitive (legacy_session_helpers_test.go).
func dualEraLegacyInitErr(mcpClient *e2e.RawMCPClient, url string) (string, error) {
	return legacySessionInit(mcpClient, url, "dual-era-redis-legacy-client", nil)
}

// dualEraLegacyListTools sends a Legacy tools/list on sessionID and returns
// the aggregated tool names. It delegates to the shared legacySessionListTools
// primitive (legacy_session_helpers_test.go).
//
// Returns an error rather than using Expect/Ginkgo assertions so it is safe
// to call from inside an Eventually retry loop (e.g. the BeforeAll warm-up).
func dualEraLegacyListTools(mcpClient *e2e.RawMCPClient, url, sessionID string) ([]string, error) {
	return legacySessionListTools(mcpClient, url, sessionID, nil)
}

// dualEraLegacyCall sends a Legacy tools/call for toolName on sessionID,
// carrying the MCP-Protocol-Version header the 2025-11-25 spec requires on
// every post-initialize request. Mirrors legacyToolCall in
// vmcp_dual_era_test.go.
//
// input MUST match yardstick's echo tool's input schema, "^[a-zA-Z0-9]+$" --
// no hyphens, underscores, or dots. A non-matching input is NOT rejected as a
// JSON-RPC error: yardstick returns it as a successful (HTTP 200, resp.Error
// == nil) result with "isError": true and a validation-failure message in the
// content, so resp.Error being nil does NOT prove the call worked -- callers
// must inspect the result body (see expectDualEraEcho). Build any per-call
// unique nonce as e.g. fmt.Sprintf("round%dreq%d", round, i), never with a
// separator character.
func dualEraLegacyCall(mcpClient *e2e.RawMCPClient, url, sessionID, toolName, input string) *e2e.RawResponse {
	GinkgoHelper()
	resp, err := dualEraLegacyCallErr(mcpClient, url, sessionID, toolName, input)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

// dualEraLegacyCallErr is the error-returning variant of dualEraLegacyCall,
// for use inside an Eventually/Consistently retry loop (e.g. the Redis-outage
// spec's 503 poll), where a transient Send failure must be retried rather
// than aborting the spec, and inside the concurrency spec's goroutines, where
// a failing Expect would panic the whole test process rather than fail the
// spec (no GinkgoRecover on that goroutine's stack). mcpClient is taken as a
// parameter rather than captured, which is what makes calling this from
// concurrent goroutines safe in the first place (RawMCPClient documents
// itself as safe for concurrent use, mcp_raw_client.go ~:287-290). It
// deliberately does NOT inspect resp.StatusCode/Error -- a non-2xx response
// (503, 404, ...) is a normal, successfully-received HTTP response, not a Go
// error; only an actual transport failure from Send (e.g. connection
// refused) is surfaced as the returned error.
func dualEraLegacyCallErr(mcpClient *e2e.RawMCPClient, url, sessionID, toolName, input string) (*e2e.RawResponse, error) {
	return legacySessionCallTool(mcpClient, url, sessionID, toolName, map[string]any{"input": input}, nil)
}

// dualEraModernCall sends a stateless Modern tools/call for toolName. Mirrors
// modernToolCall in vmcp_dual_era_test.go.
//
// input is subject to the same "^[a-zA-Z0-9]+$" echo-tool constraint as
// dualEraLegacyCall's input -- see that doc comment for the nonce-building
// guidance and the isError-but-200 gotcha.
//
// CRITICAL: neither this nor dualEraLegacyCall/dualEraLegacyInit ever calls
// WithStreamableAccept(). vMCP does not require that Accept header, and
// setting it switches vMCP to an SSE response body that this client's
// plain-JSON parser cannot read (mcp_raw_client.go); e2e.NewModernRequest
// already emits the full conformant Modern wire shape otherwise.
//
// Aborts the spec via Expect on failure. Use dualEraModernCallErr instead
// inside the concurrency spec's goroutines, where a failing Expect would
// panic the process rather than fail the spec.
func dualEraModernCall(mcpClient *e2e.RawMCPClient, url, toolName, input string) *e2e.RawResponse {
	GinkgoHelper()
	resp, err := dualEraModernCallErr(mcpClient, url, toolName, input)
	Expect(err).ToNot(HaveOccurred())
	return resp
}

// dualEraModernCallErr is the error-returning variant of dualEraModernCall,
// for use inside the concurrency spec's goroutines: a Fail() (what a failing
// Expect does) on a goroutine with no GinkgoRecover on its stack panics the
// whole test process, not just this spec, so those goroutines must call this
// instead and let the main goroutine assert on the returned error after the
// join. Mirrors dualEraLegacyCallErr's split from dualEraLegacyCall.
func dualEraModernCallErr(mcpClient *e2e.RawMCPClient, url, toolName, input string) (*e2e.RawResponse, error) {
	req, err := e2e.NewModernRequest("tools/call", map[string]any{
		"name":      toolName,
		"arguments": map[string]any{"input": input},
	})
	if err != nil {
		return nil, fmt.Errorf("build tools/call: %w", err)
	}
	return mcpClient.Send(context.Background(), url, req)
}

// expectDualEraEcho asserts a tools/call response succeeded, round-tripped
// wantOutput, and carries the era-correct resultType. resultType is present
// ONLY on a Modern client's own response envelope ("complete"); it is empty
// for a Legacy client, regardless of which era backend actually served the
// call. Mirrors assertBridgedCall + echoResult in vmcp_dual_era_test.go.
//
// This proves a round-trip to *a* backend with the era-correct envelope; it
// does NOT by itself prove WHICH backend answered -- both backends run the
// same yardstick echo tool, so a misrouted call would echo the same value
// back. Backend attribution is proven separately, by the era-gate /status
// check in this file's BeforeAll.
func expectDualEraEcho(resp *e2e.RawResponse, wantOutput, wantResultType string) {
	GinkgoHelper()
	Expect(dualEraEchoErr(resp, wantOutput, wantResultType)).To(Succeed())
}

// dualEraEchoErr is the error-returning counterpart of expectDualEraEcho, for
// use inside a Consistently/Eventually poll (e.g. the Redis-outage spec's
// Modern-path check) where a mismatch must be reported back to the poller
// instead of aborting the whole spec via Expect.
func dualEraEchoErr(resp *e2e.RawResponse, wantOutput, wantResultType string) error {
	if resp.Error != nil {
		return fmt.Errorf("unexpected JSON-RPC error: %+v", resp.Error)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status %d, body: %s", resp.StatusCode, resp.Body)
	}

	var result struct {
		ResultType        string `json:"resultType"`
		StructuredContent struct {
			Output string `json:"output"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("unmarshal result: %w, raw: %s", err, resp.Result)
	}
	// Include the raw result in both messages: an empty Output is the common
	// failure and says nothing on its own about whether the call reached the
	// wrong tool, echoed into a different field, or returned no content at all.
	if result.StructuredContent.Output != wantOutput {
		return fmt.Errorf("tools/call round-trip did not echo the expected output %q, got %q; raw result: %s",
			wantOutput, result.StructuredContent.Output, resp.Result)
	}
	if result.ResultType != wantResultType {
		return fmt.Errorf("unexpected resultType %q, want %q; raw result: %s",
			result.ResultType, wantResultType, resp.Result)
	}
	return nil
}

// vmcpContainerRestartCount returns the restart count of pod's "vmcp"
// container -- the container name this file's BeforeAll pod-template env
// injection targets. Fails the spec loudly (rather than returning a zero
// value) if no such container status exists, since that would silently
// defeat the Redis-outage spec's restart-count assertion.
func vmcpContainerRestartCount(pod *corev1.Pod) int32 {
	GinkgoHelper()
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "vmcp" {
			return cs.RestartCount
		}
	}
	Fail(fmt.Sprintf("pod %s has no %q container status", pod.Name, "vmcp"))
	return 0
}
