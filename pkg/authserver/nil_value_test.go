// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
)

func TestIsNilValue(t *testing.T) {
	t.Parallel()

	var nilPointer *mocks.MockActorTokenValidator
	var nilMap map[string]int
	var nilSlice []int
	var nilFunc func()
	var nilChan chan int

	tests := []struct {
		name string
		v    any
		want bool
	}{
		{name: "nil interface", v: nil, want: true},
		{name: "typed nil pointer", v: nilPointer, want: true},
		{name: "non-nil pointer", v: &struct{}{}, want: false},
		{name: "nil map", v: nilMap, want: true},
		{name: "non-nil map", v: map[string]int{}, want: false},
		{name: "nil slice", v: nilSlice, want: true},
		{name: "non-nil slice", v: []int{}, want: false},
		{name: "nil func", v: nilFunc, want: true},
		{name: "non-nil func", v: func() {}, want: false},
		{name: "nil chan", v: nilChan, want: true},
		{name: "non-nil chan", v: make(chan int), want: false},
		{name: "non-nil struct value", v: struct{}{}, want: false},
		{name: "zero int", v: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isNilValue(tt.v))
		})
	}
}
