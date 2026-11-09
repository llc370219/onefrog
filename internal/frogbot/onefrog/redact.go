package onefrog

import "encoding/json"

// RedactJSONStrings applies a host redactor to text inside an event without
// corrupting the JSON encoding if a credential contains quotes or newlines.
func RedactJSONStrings(raw []byte, redact func(string) string) []byte {
	if redact == nil {
		return raw
	}
	var value json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	var visit func(json.RawMessage) json.RawMessage
	visit = func(v json.RawMessage) json.RawMessage {
		switch v[0] {
		case '"':
			var text string
			_ = json.Unmarshal(v, &text)
			v, _ = json.Marshal(redact(text))
		case '{':
			var obj map[string]json.RawMessage
			_ = json.Unmarshal(v, &obj)
			for k, item := range obj {
				obj[k] = visit(item)
			}
			v, _ = json.Marshal(obj)
		case '[':
			var items []json.RawMessage
			_ = json.Unmarshal(v, &items)
			for i := range items {
				items[i] = visit(items[i])
			}
			v, _ = json.Marshal(items)
		}
		return v
	}
	return visit(value)
}
