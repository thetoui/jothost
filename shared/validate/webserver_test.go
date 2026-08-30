package validate

import (
	"errors"
	"testing"
)

func TestWebserverMode(t *testing.T) {
	for _, mode := range []string{WebserverNginx, WebserverHybrid} {
		if err := WebserverMode(mode); err != nil {
			t.Errorf("WebserverMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []string{"", "apache", "Nginx", "hybrid "} {
		if err := WebserverMode(mode); !errors.Is(err, ErrInvalidWebserverMode) {
			t.Errorf("WebserverMode(%q) = %v, want ErrInvalidWebserverMode", mode, err)
		}
	}
}

func TestBackendPortStaysInItsOwnRange(t *testing.T) {
	for _, port := range []int{MinBackendPort, 7500, MaxBackendPort} {
		if err := BackendPort(port); err != nil {
			t.Errorf("BackendPort(%d) = %v, want nil", port, err)
		}
	}

	// 80 and 443 belong to nginx, 0 and negative are not ports at all, and
	// anything from 32768 up is the range the kernel hands to outgoing
	// connections — a backend there binds fine most days and fails on the day
	// something else got there first.
	for _, port := range []int{0, -1, 80, 443, 1023, 7079, 7980, 32768, 65536} {
		if err := BackendPort(port); !errors.Is(err, ErrInvalidBackendPort) {
			t.Errorf("BackendPort(%d) = %v, want ErrInvalidBackendPort", port, err)
		}
	}
}

// The backend range must stay clear of the ephemeral range, which is also what
// AppPort refuses for a Node application.
func TestBackendRangeIsBelowTheEphemeralFloor(t *testing.T) {
	if MaxBackendPort >= EphemeralFloor {
		t.Fatalf("the backend range reaches %d, at or above the ephemeral floor %d",
			MaxBackendPort, EphemeralFloor)
	}
	if MinBackendPort <= MaxAppPort && MinBackendPort < 1024 {
		t.Fatal("the backend range reaches into the privileged ports")
	}
	if BackendPortCount != MaxBackendPort-MinBackendPort+1 {
		t.Fatal("BackendPortCount does not describe the range")
	}
}
