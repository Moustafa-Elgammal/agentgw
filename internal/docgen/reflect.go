package docgen

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// jsonField is one JSON field of a Go struct.
type jsonField struct {
	Name      string
	Type      string
	OmitEmpty bool
}

// jsonFields lists the JSON fields of struct type t in declaration order. Embedded structs
// are flattened, like encoding/json does.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" && f.Type.Kind() == reflect.Struct {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out = append(out, jsonField{Name: name, Type: jsonType(f.Type), OmitEmpty: strings.Contains(opts, "omitempty")})
	}
	return out
}

var timeType = reflect.TypeOf(time.Time{})

// jsonType names the JSON type a Go type encodes to.
func jsonType(t reflect.Type) string {
	if t == timeType {
		return "string (RFC 3339 time)"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return jsonType(t.Elem())
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array of " + jsonType(t.Elem())
	case reflect.Map:
		return "object"
	case reflect.Struct:
		return "object"
	}
	panic(fmt.Sprintf("docgen: no JSON type for %s", t))
}
