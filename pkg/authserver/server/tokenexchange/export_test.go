// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

// ValidateActorForTest exposes validateActor to the external test package, which
// needs the generated mock package that itself imports tokenexchange.
var ValidateActorForTest = validateActor

// ActorValidationTimeoutForTest is the production validation budget, so tests
// that are not about the timeout run with the same budget the server uses.
const ActorValidationTimeoutForTest = actorValidationTimeout
