package gcp

import (
	"testing"
)

func TestSanitizeZoneName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple cluster name",
			input:    "my-cluster.hypershift.local",
			expected: "my-cluster-hypershift-local",
		},
		{
			name:     "cluster name with trailing dot",
			input:    "my-cluster.hypershift.local.",
			expected: "my-cluster-hypershift-local",
		},
		{
			name:     "cluster name with multiple segments",
			input:    "prod-cluster-01.hypershift.local",
			expected: "prod-cluster-01-hypershift-local",
		},
		{
			name:     "complex cluster name",
			input:    "my-very-long-cluster-name.hypershift.local",
			expected: "my-very-long-cluster-name-hypershift-local",
		},
		{
			name:     "single segment without dots",
			input:    "simple",
			expected: "simple",
		},
		{
			name:     "already sanitized name",
			input:    "my-cluster-hypershift-local",
			expected: "my-cluster-hypershift-local",
		},
		{
			name:     "name with subdomain",
			input:    "cluster.sub.example.com",
			expected: "cluster-sub-example-com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeZoneName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeZoneName(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeZoneName_Idempotent(t *testing.T) {
	input := "my-cluster.hypershift.local"
	firstRun := sanitizeZoneName(input)
	secondRun := sanitizeZoneName(firstRun)

	if firstRun != secondRun {
		t.Errorf("sanitizeZoneName is not idempotent: first=%q, second=%q", firstRun, secondRun)
	}
}

func TestHypershiftLocalZoneName(t *testing.T) {
	expected := "hypershift.local"
	if hypershiftLocalZoneName != expected {
		t.Errorf("hypershiftLocalZoneName = %q, expected %q", hypershiftLocalZoneName, expected)
	}
}
