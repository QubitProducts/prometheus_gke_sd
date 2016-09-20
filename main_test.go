package main

import (
	"testing"

	container "google.golang.org/api/container/v1"
)

func TestClusterListEqual(t *testing.T) {
	t.Parallel()

	cases := []struct {
		old, new []*container.Cluster
		expected bool
	}{
		{
			old:      []*container.Cluster{},
			new:      []*container.Cluster{},
			expected: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			t.Parallel()

			result := clusterListEqual(c.old, c.new)
			if result != c.expected {
				t.Fatalf("Difference in expected result\nGot: %v\nExpected: %v\n", result, c.expected)
			}
		})
	}
}
