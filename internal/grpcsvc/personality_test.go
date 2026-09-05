package grpcsvc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"src.solsynth.dev/sosys/personality/internal/service"
)

func TestMapError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"not found", service.ErrNotFound, codes.NotFound},
		{"forbidden", service.ErrForbidden, codes.PermissionDenied},
		{"deadline exceeded", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"wrapped deadline exceeded", fmt.Errorf("generation failed: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{"canceled", context.Canceled, codes.Canceled},
		{"wrapped canceled", fmt.Errorf("generation failed: %w", context.Canceled), codes.Canceled},
		{"unclassified", errors.New("boom"), codes.InvalidArgument},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := status.Code(mapError(tc.err)); got != tc.want {
				t.Fatalf("mapError(%v) code = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
