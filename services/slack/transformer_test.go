package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateGuestHandling(t *testing.T) {
	t.Run("accepts supported modes", func(t *testing.T) {
		for _, mode := range []string{GuestHandlingGuest, GuestHandlingUser, GuestHandlingSkip} {
			require.NoError(t, ValidateGuestHandling(mode), "mode %q should be valid", mode)
		}
	})

	t.Run("rejects unknown modes", func(t *testing.T) {
		for _, mode := range []string{"", "Guest", "member", "drop"} {
			require.Error(t, ValidateGuestHandling(mode), "mode %q should be invalid", mode)
		}
	})
}
