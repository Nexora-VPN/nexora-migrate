package dbfetch

import "encoding/json"

// jsonUnquote reads a JSON string, tolerating a panel that answers with a bare
// value where the field is documented as an object.
func jsonUnquote(raw json.RawMessage, out *string) error {
	if err := json.Unmarshal(raw, out); err == nil {
		return nil
	}
	var probe map[string]string
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	for _, k := range []string{"token", "csrfToken", "value"} {
		if v := probe[k]; v != "" {
			*out = v
			return nil
		}
	}
	return nil
}
