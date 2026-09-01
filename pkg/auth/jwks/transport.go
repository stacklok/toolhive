// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package jwks

import "net/http"

// limitedBodyTransport wraps an http.RoundTripper to cap every response body
// at max bytes, via http.MaxBytesReader rather than io.LimitReader: the
// latter truncates silently, which would let a caller parse a cut-off JWKS
// document as if it were complete, where the former surfaces a
// *http.MaxBytesError instead. Its error text ("http: request body too
// large") is written for the request-body case MaxBytesReader was designed
// for, so it reads oddly for a capped response — an accepted rough edge
// rather than justifying a custom ReadCloser.
//
// The nil first argument (http.ResponseWriter) is safe: MaxBytesReader only
// reaches it through a type assertion (`l.w.(requestTooLarger)`) used to tell
// a real server connection to close early, which — on a nil interface value
// — safely evaluates to false rather than panicking (net/http/request.go).
type limitedBodyTransport struct {
	base http.RoundTripper
	max  int64
}

// RoundTrip delegates to base and then caps the returned body. The cap
// applies to every response, including a non-2xx one whose body the caller
// doesn't intend to parse — draining it is only ever io.Discard-ed, not
// unbounded, so this errs on the safe side rather than special-casing status
// codes.
func (t *limitedBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.max)
	return resp, nil
}
