package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShouldDeleteTemporarySource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		temporary  bool
		finalTry   bool
		processErr error
		want       bool
	}{
		{"permanent source", false, true, nil, false},
		{"successful initial worker leaves source for asynchronous multimodal work", true, false, nil, false},
		{"retryable attempt", true, false, errors.New("temporary network error"), false},
		{"final failed attempt", true, true, errors.New("still unavailable"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shouldDeleteTemporarySource(tc.temporary, tc.finalTry, tc.processErr))
		})
	}
}
