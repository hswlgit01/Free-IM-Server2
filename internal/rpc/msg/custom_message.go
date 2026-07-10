package msg

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

type parsedCustomMessage struct {
	Type               int
	Data               json.RawMessage
	ConflictingWrapper bool
}

func parseCustomMessage(content []byte) (parsedCustomMessage, bool) {
	return parseCustomMessageDepth(content, 0)
}

func parseCustomMessageDepth(content []byte, depth int) (parsedCustomMessage, bool) {
	if depth > 1 {
		return parsedCustomMessage{}, false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return parsedCustomMessage{}, false
	}
	var (
		outer        parsedCustomMessage
		hasOuter     bool
		invalidOuter bool
		nested       parsedCustomMessage
		hasNested    bool
	)
	if rawType, ok := fields["customType"]; ok {
		typeValue, ok := parseCustomType(rawType)
		if !ok {
			invalidOuter = true
		} else {
			outer = parsedCustomMessage{Type: typeValue, Data: fields["data"]}
			hasOuter = true
		}
	}

	// Preserve compatibility with older wrappers that put a JSON string in one
	// of these fields. Standard call invitations use top-level customType/data.
	for _, fieldName := range []string{"data", "detail", "content"} {
		raw, ok := fields[fieldName]
		if !ok {
			continue
		}
		var nestedJSON string
		if err := json.Unmarshal(raw, &nestedJSON); err != nil {
			continue
		}
		if parsed, ok := parseCustomMessageDepth([]byte(nestedJSON), depth+1); ok {
			nested = parsed
			hasNested = true
			break
		}
	}
	if hasNested && (invalidOuter || nested.ConflictingWrapper || (hasOuter && outer.Type != nested.Type)) {
		return parsedCustomMessage{ConflictingWrapper: true}, true
	}
	// A string wrapper is what the SDK exposes as CustomElem.Data, so when it
	// contains a valid custom message it is authoritative. Equal outer/inner
	// types are accepted; conflicting types are rejected above.
	if hasNested {
		return nested, true
	}
	if hasOuter {
		return outer, true
	}
	return parsedCustomMessage{}, false
}

func parseCustomType(raw json.RawMessage) (int, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}

	var stringValue string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &stringValue); err != nil {
			return 0, false
		}
		value, err := strconv.Atoi(stringValue)
		return value, err == nil
	}

	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	intValue := int(value)
	if float64(intValue) != value {
		return 0, false
	}
	return intValue, true
}
