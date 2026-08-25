package operations

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/protocol"
)

// structToMap converts a typed result into the protocol's generic data map.
//
// Encoding through the struct's JSON tags keeps one definition of every field
// name, so the wire format cannot drift from the Go type it came from.
func structToMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("decode result: %w", err)
	}
	return out, nil
}

// mountPointMaxLength bounds a caller-supplied mount point.
const mountPointMaxLength = 4096

// validateMountPoint rejects malformed mount points before they are compared
// against the mount table.
func validateMountPoint(path string) error {
	switch {
	case !strings.HasPrefix(path, "/"):
		return Fail(protocol.CodeInvalidPayload, "mount_point must be an absolute path", nil)
	case len(path) > mountPointMaxLength:
		return Fail(protocol.CodeInvalidPayload, "mount_point is too long", nil)
	case strings.ContainsRune(path, '\x00'):
		return Fail(protocol.CodeInvalidPayload, "mount_point contains a null byte", nil)
	}

	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return Fail(protocol.CodeInvalidPayload, "mount_point must not contain a traversal segment", nil)
		}
	}
	return nil
}
