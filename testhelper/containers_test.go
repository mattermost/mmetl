package testhelper

import (
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindHighestStableVersion(t *testing.T) {
	tests := []struct {
		name        string
		tags        []string
		expected    string
		expectError bool
	}{
		{
			name:     "normal versions",
			tags:     []string{"10.5.0", "10.5.1", "10.6.0", "10.4.3"},
			expected: "10.6.0",
		},
		{
			name:     "excludes release candidates",
			tags:     []string{"10.5.0", "10.6.0-rc1", "10.6.0-RC2", "10.5.1"},
			expected: "10.5.1",
		},
		{
			name:     "excludes short tags",
			tags:     []string{"10.5", "10", "latest", "10.5.0"},
			expected: "10.5.0",
		},
		{
			name:     "excludes branch tags",
			tags:     []string{"master", "release-10.5", "10.5.0", "develop"},
			expected: "10.5.0",
		},
		{
			name:        "empty input",
			tags:        []string{},
			expectError: true,
		},
		{
			name:        "no valid versions",
			tags:        []string{"latest", "master", "10.5", "10.6.0-rc1"},
			expectError: true,
		},
		{
			name:     "single version",
			tags:     []string{"10.5.0"},
			expected: "10.5.0",
		},
		{
			name:     "many versions picks highest",
			tags:     []string{"9.0.0", "10.0.0", "10.11.10", "10.11.9", "11.0.0", "10.12.4"},
			expected: "11.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := findHighestStableVersion(tt.tags)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFetchLatestStableTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tag, err := fetchLatestStableTag(defaultMattermostImage)
	require.NoError(t, err)
	require.NotEmpty(t, tag)

	v, err := semver.Parse(tag)
	require.NoError(t, err, "result %q should be valid semver", tag)
	assert.True(t, v.GTE(semver.MustParse("10.0.0")), "expected version >= 10.0.0, got %s", tag)
}
