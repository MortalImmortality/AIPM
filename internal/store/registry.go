package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strings"

	"aipm/internal/provider"
)

type Registry struct {
	Version   int                       `json:"version"`
	Providers []provider.Provider       `json:"providers"`
	Snapshots map[string]map[string]any `json:"snapshots"`
}

func emptyRegistry() Registry {
	return Registry{Version: 1, Providers: []provider.Provider{}, Snapshots: map[string]map[string]any{}}
}

// value consumes tokens so duplicate members cannot be silently discarded by
// encoding/json. Error messages deliberately never include input or keys.
func value(d *json.Decoder, depth int) (any, error) {
	if depth > 128 {
		return nil, errors.New("invalid JSON")
	}
	t, err := d.Token()
	if err != nil {
		return nil, errors.New("invalid JSON")
	}
	switch t {
	case json.Delim('{'):
		obj := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, errors.New("invalid JSON")
			}
			k, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid JSON")
			}
			if _, ok = obj[k]; ok {
				return nil, errors.New("duplicate JSON member")
			}
			v, e := value(d, depth+1)
			if e != nil {
				return nil, e
			}
			obj[k] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errors.New("invalid JSON")
		}
		return obj, nil
	case json.Delim('['):
		arr := []any{}
		for d.More() {
			v, e := value(d, depth+1)
			if e != nil {
				return nil, e
			}
			arr = append(arr, v)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, errors.New("invalid JSON")
		}
		return arr, nil
	}
	if _, ok := t.(json.Delim); ok {
		return nil, errors.New("invalid JSON")
	}
	return t, nil
}

// checkType also rejects null for typed fields (JSON decoding alone accepts it).
func checkType(v any, t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface:
		return true
	case reflect.String:
		_, ok := v.(string)
		return ok
	case reflect.Bool:
		_, ok := v.(bool)
		return ok
	case reflect.Int:
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return false
		}
		for _, x := range a {
			if !checkType(x, t.Elem()) {
				return false
			}
		}
		return true
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, x := range m {
			if !checkType(x, t.Elem()) {
				return false
			}
		}
		return true
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			fields[tag[0]] = f.Type
			if len(tag) == 1 {
				if _, exists := m[tag[0]]; !exists {
					return false
				}
			}
		}
		for k, x := range m {
			ft, ok := fields[k]
			if !ok || !checkType(x, ft) {
				return false
			}
		}
		return true
	}
	return false
}

func strict(data []byte, out any) error {
	if len(data) > maxBytes {
		return errors.New("JSON exceeds 4 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := value(d, 0)
	if err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("invalid JSON")
	}
	if !checkType(v, reflect.TypeOf(out).Elem()) {
		return errors.New("invalid JSON field type or unknown field")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return errors.New("invalid JSON")
	}
	return nil
}

func decodeRegistry(data []byte) (Registry, error) {
	var r Registry
	if err := strict(data, &r); err != nil {
		return Registry{}, err
	}
	if r.Providers == nil || r.Snapshots == nil {
		return Registry{}, errors.New("missing registry fields")
	}
	if _, err := canonical(r); err != nil {
		return Registry{}, err
	}
	return r, nil
}

func canonical(r Registry) ([]byte, error) {
	if r.Version != 1 {
		return nil, errors.New("unsupported registry version")
	}
	if r.Providers == nil {
		r.Providers = []provider.Provider{}
	}
	if r.Snapshots == nil {
		r.Snapshots = map[string]map[string]any{}
	}
	seen := map[string]bool{}
	for _, p := range r.Providers {
		if err := p.Validate(); err != nil {
			return nil, errors.New("invalid provider")
		}
		if seen[p.ID] {
			return nil, errors.New("duplicate provider ID")
		}
		seen[p.ID] = true
	}
	for _, snapshot := range r.Snapshots {
		if snapshot == nil {
			return nil, errors.New("invalid snapshot")
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, errors.New("invalid registry value")
	}
	if len(b) > maxBytes {
		return nil, errors.New("registry exceeds 4 MiB")
	}
	// Ensure in-memory arbitrary snapshot values obey the persisted JSON shape.
	var check Registry
	if err = strict(b, &check); err != nil {
		return nil, err
	}
	return b, nil
}

// JSON number spelling is not significant to CAS (1, 1.0, and 1e0).
// Represent numbers as exact decimal coefficient/exponent pairs, without
// expanding huge exponents or losing precision through float64.
func semanticJSON(v any) any {
	switch x := v.(type) {
	case Registry:
		for k, snapshot := range x.Snapshots {
			normalized := make(map[string]any, len(snapshot))
			for key, val := range snapshot {
				normalized[key] = semanticJSON(val)
			}
			x.Snapshots[k] = normalized
		}
		return x
	case json.Number:
		text := string(x)
		sign := ""
		if strings.HasPrefix(text, "-") {
			sign = "-"
			text = text[1:]
		}
		exponent := new(big.Int)
		if i := strings.IndexAny(text, "eE"); i >= 0 {
			exponent.SetString(text[i+1:], 10)
			text = text[:i]
		}
		decimals := 0
		if i := strings.IndexByte(text, '.'); i >= 0 {
			decimals = len(text) - i - 1
			text = text[:i] + text[i+1:]
		}
		text = strings.TrimLeft(text, "0")
		if text == "" {
			return map[string]any{"number": []string{"0", "0"}}
		}
		trimmed := strings.TrimRight(text, "0")
		exponent.Add(exponent, big.NewInt(int64(len(text)-len(trimmed)-decimals)))
		return map[string]any{"number": []string{sign + trimmed, exponent.String()}}
	case map[string]any:
		normalized := make(map[string]any, len(x))
		for k, val := range x {
			normalized[k] = semanticJSON(val)
		}
		return map[string]any{"object": normalized}
	case []any:
		normalized := make([]any, len(x))
		for i, val := range x {
			normalized[i] = semanticJSON(val)
		}
		return map[string]any{"array": normalized}
	}
	return v
}
