package operations

import (
	"bytes"
	"encoding/json"

	"github.com/jothost/panel/shared/protocol"
)

// decodePayload converts an operation's payload into a typed struct.
//
// The round trip through JSON is deliberate: it gives strict typing and
// unknown-field rejection for free, so a payload with a misspelled or
// unexpected key is refused rather than silently ignored. A handler that
// silently ignores an argument is how a "read /var/www" turns into a "read /".
func decodePayload(req protocol.Request, dst any) error {
	if len(req.Payload) == 0 {
		// An operation with no arguments is valid; dst keeps its zero value.
		return nil
	}

	encoded, err := json.Marshal(req.Payload)
	if err != nil {
		return Fail(protocol.CodeInvalidPayload, "Payload could not be read", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		// The error text can echo caller input, so it travels as Cause (for
		// the log) rather than as the client-facing message.
		return Fail(protocol.CodeInvalidPayload, "Payload is not valid for this operation", err)
	}
	return nil
}
