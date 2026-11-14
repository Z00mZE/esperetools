package kvstorage

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Value supports scalars and slices of built-in scalar types. byte and rune
// are aliases of uint8 and int32. Nested slices, pointers and structs are excluded.
type Value interface {
	~bool | ~string |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 |
		~[]bool | ~[]string |
		~[]int | ~[]int8 | ~[]int16 | ~[]int32 | ~[]int64 |
		~[]uint | ~[]uint8 | ~[]uint16 | ~[]uint32 | ~[]uint64 |
		~[]float32 | ~[]float64
}

// The envelope lives inside the existing byte-value format, so old snapshots
// remain readable. The last byte is the serialization version; the next byte
// is a stable scalar type code, with bit 7 set for slices.
var valueMagic = [8]byte{0, 'K', 'V', 'J', 'S', 'O', 'N', 1}

const (
	kindBool byte = iota + 1
	kindString
	kindInt
	kindInt8
	kindInt16
	kindInt32
	kindInt64
	kindUint
	kindUint8
	kindUint16
	kindUint32
	kindUint64
	kindFloat32
	kindFloat64
	kindSlice byte = 0x80
)

// Use explicit codes instead of persisting Go's reflect.Kind numbers.
func valueKind[T Value]() byte {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	var flag byte
	if typ.Kind() == reflect.Slice {
		flag = kindSlice
		typ = typ.Elem()
	}
	var kind byte
	switch typ.Kind() {
	case reflect.Bool:
		kind = kindBool
	case reflect.String:
		kind = kindString
	case reflect.Int:
		kind = kindInt
	case reflect.Int8:
		kind = kindInt8
	case reflect.Int16:
		kind = kindInt16
	case reflect.Int32:
		kind = kindInt32
	case reflect.Int64:
		kind = kindInt64
	case reflect.Uint:
		kind = kindUint
	case reflect.Uint8:
		kind = kindUint8
	case reflect.Uint16:
		kind = kindUint16
	case reflect.Uint32:
		kind = kindUint32
	case reflect.Uint64:
		kind = kindUint64
	case reflect.Float32:
		kind = kindFloat32
	case reflect.Float64:
		kind = kindFloat64
	}
	return flag | kind
}

// Set serializes and persists a typed value. It does not modify storage if
// serialization fails. Changing the type stored at an existing key is allowed.
func (s *Store) Set[T Value](key string, value T) error {
	if !validKey(key) {
		return ErrInvalidKey
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b := make([]byte, len(valueMagic)+1+len(data))
	copy(b, valueMagic[:])
	b[len(valueMagic)] = valueKind[T]()
	copy(b[len(valueMagic)+1:], data)
	return s.SetBytes(key, b)
}

// Get returns the typed value, its presence, and an error. A missing key
// returns the zero value, false, nil. On an error it returns zero, false, err.
// A different stored scalar type (including slice element type) returns
// ErrTypeMismatch rather than silently converting or losing numeric precision.
func (s *Store) Get[T Value](key string) (T, bool, error) {
	var value T
	b, found, err := s.GetBytes(key)
	if err != nil || !found {
		return value, false, err
	}
	if len(b) >= len(valueMagic) && bytes.Equal(b[:len(valueMagic)-1], valueMagic[:len(valueMagic)-1]) {
		if b[len(valueMagic)-1] != valueMagic[len(valueMagic)-1] {
			return value, false, ErrUnsupported
		}
		if len(b) < len(valueMagic)+1 {
			return value, false, ErrCorrupt
		}
		if b[len(valueMagic)] != valueKind[T]() {
			return value, false, ErrTypeMismatch
		}
		if err := json.Unmarshal(b[len(valueMagic)+1:], &value); err != nil {
			var zero T
			return zero, false, err
		}
		return value, true, nil
	}
	// Older snapshots contain raw bytes, without a type tag. Preserve both
	// legacy string and byte-slice reads; never guess a numeric or bool type.
	switch valueKind[T]() {
	case kindString:
		b, err = json.Marshal(string(b))
	case kindSlice | kindUint8:
		b, err = json.Marshal(b)
	default:
		return value, false, ErrTypeMismatch
	}
	if err != nil {
		return value, false, err
	}
	if err := json.Unmarshal(b, &value); err != nil {
		var zero T
		return zero, false, err
	}
	return value, true, nil
}
