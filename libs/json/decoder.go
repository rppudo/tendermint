package json

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

func decodeJSON(bz []byte, v interface{}) error {
	if len(bz) == 0 {
		return errors.New("cannot decode empty bytes")
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr {
		return errors.New("must decode into a pointer")
	}
	rv = rv.Elem()
	return decodeJSONReflect(bz, rv)
}

func decodeJSONReflect(bz []byte, rv reflect.Value) error {
	if !rv.CanAddr() {
		return errors.New("value is not addressable")
	}

	// Handle null for slices, interfaces, and pointers
	if bytes.Equal(bz, []byte("null")) {
		rv.Set(reflect.Zero(rv.Type()))
		return nil
	}

	// Dereference-and-construct pointers, to handle nested pointers.
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		rv = rv.Elem()
	}

	// Times must be UTC and end with Z
	if rv.Type() == timeType {
		switch {
		case len(bz) < 2 || bz[0] != '"' || bz[len(bz)-1] != '"':
			return fmt.Errorf("JSON time must be an RFC3339 string, but got %s", bz)
		case bz[len(bz)-2] != 'Z':
			return fmt.Errorf("JSON time must be UTC and end with 'Z', but got %s", bz)
		}
	}

	// If value implements json.Umarshaler, call it.
	if rv.Addr().Type().Implements(jsonUnmarshalerType) {
		return rv.Addr().Interface().(json.Unmarshaler).UnmarshalJSON(bz)
	}

	switch rv.Type().Kind() {
	// Decode complex types
	//case reflect.Interface:
	//	err = cdc.decodeReflectJSONInterface(bz, info, rv, fopts)

	case reflect.Slice, reflect.Array:
		return decodeJSONReflectList(bz, rv)

	case reflect.Map:
		return decodeJSONReflectMap(bz, rv)

	case reflect.Struct:
		return decodeJSONReflectStruct(bz, rv)

	// For 64-bit integers, unwrap expected string and defer to stdlib for integer decoding.
	case reflect.Int64, reflect.Int, reflect.Uint64, reflect.Uint:
		if bz[0] != '"' || bz[len(bz)-1] != '"' {
			return fmt.Errorf("invalid 64-bit integer encoding %q, expected string", string(bz))
		}
		bz = bz[1 : len(bz)-1]
		fallthrough

	// Anything else we defer to the stdlib.
	default:
		return decodeJSONStdlib(bz, rv)
	}
}

func decodeJSONReflectList(bz []byte, rv reflect.Value) error {
	if !rv.CanAddr() {
		return errors.New("list value is not addressable")
	}

	switch rv.Type().Elem().Kind() {
	// Decode base64-encoded bytes using stdlib decoder, via byte slice for arrays.
	case reflect.Uint8:
		if rv.Type().Kind() == reflect.Array {
			var buf []byte
			if err := json.Unmarshal(bz, &buf); err != nil {
				return err
			}
			if len(buf) != rv.Len() {
				return fmt.Errorf("got %v bytes, expected %v", len(buf), rv.Len())
			}
			reflect.Copy(rv, reflect.ValueOf(buf))

		} else if err := decodeJSONStdlib(bz, rv); err != nil {
			return err
		}

	// Decode anything else into a raw JSON slice, and decode values recursively.
	default:
		var rawSlice []json.RawMessage
		if err := json.Unmarshal(bz, &rawSlice); err != nil {
			return err
		}
		if rv.Type().Kind() == reflect.Slice {
			rv.Set(reflect.MakeSlice(reflect.SliceOf(rv.Type().Elem()), len(rawSlice), len(rawSlice)))
		}
		if rv.Len() != len(rawSlice) { // arrays of wrong size
			return fmt.Errorf("got list of %v elements, expected %v", len(rawSlice), rv.Len())
		}
		for i, bz := range rawSlice {
			if err := decodeJSONReflect(bz, rv.Index(i)); err != nil {
				return err
			}
		}
	}

	// Replace empty slices with nil slices, for Amino compatibility
	if rv.Type().Kind() == reflect.Slice && rv.Len() == 0 {
		rv.Set(reflect.Zero(rv.Type()))
	}

	return nil
}

func decodeJSONReflectMap(bz []byte, rv reflect.Value) error {
	if !rv.CanAddr() {
		return errors.New("map value is not addressable")
	}

	// Decode into a raw JSON map, using string keys.
	rawMap := make(map[string]json.RawMessage)
	if err := json.Unmarshal(bz, &rawMap); err != nil {
		return err
	}
	if rv.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("map keys must be strings, got %v", rv.Type().Key().String())
	}

	// Recursively decode values.
	rv.Set(reflect.MakeMapWithSize(rv.Type(), len(rawMap)))
	for key, bz := range rawMap {
		value := reflect.New(rv.Type().Elem()).Elem()
		if err := decodeJSONReflect(bz, value); err != nil {
			return err
		}
		rv.SetMapIndex(reflect.ValueOf(key), value)
	}
	return nil
}

func decodeJSONReflectStruct(bz []byte, rv reflect.Value) error {
	if !rv.CanAddr() {
		return errors.New("struct value is not addressable")
	}
	sInfo := makeStructInfo(rv.Type())

	// Decode raw JSON values into a string-keyed map.
	rawMap := make(map[string]json.RawMessage)
	if err := json.Unmarshal(bz, &rawMap); err != nil {
		return err
	}
	for i, fInfo := range sInfo.fields {
		frv := rv.Field(i)
		bz := rawMap[fInfo.jsonName]
		if len(bz) > 0 {
			if err := decodeJSONReflect(bz, frv); err != nil {
				return err
			}
		} else if !fInfo.omitEmpty {
			frv.Set(reflect.Zero(frv.Type()))
		}
	}

	return nil
}

func decodeJSONStdlib(bz []byte, rv reflect.Value) error {
	if !rv.CanAddr() && rv.Kind() != reflect.Ptr {
		return errors.New("value must be addressable or pointer")
	}

	// Make sure we are unmarshaling into a pointer.
	target := rv
	if rv.Kind() != reflect.Ptr {
		target = reflect.New(rv.Type())
	}
	if err := json.Unmarshal(bz, target.Interface()); err != nil {
		return err
	}
	rv.Set(target.Elem())
	return nil
}
