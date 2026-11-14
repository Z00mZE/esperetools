package kvstorage

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"
)

type timeout int64
type names []string

func typedRoundTrip[T Value](t *testing.T, value T) {
	t.Helper()
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	typed := Typed[T](s)
	must(t, typed.Set("value", value))
	writes := f.mutations
	must(t, typed.Set("value", value))
	if f.mutations != writes {
		t.Fatal("same typed value rewrote flash")
	}
	typed = Typed[T](openTest(t, f, 0, DefaultSize))
	got, found, err := typed.Get("value")
	must(t, err)
	if !found || !reflect.DeepEqual(got, value) {
		t.Fatalf("got %#v, found %v; want %#v", got, found, value)
	}
	var zero T
	got, found, err = typed.Get("absent")
	if err != nil || found || !reflect.DeepEqual(got, zero) {
		t.Fatalf("missing: %#v %v %v", got, found, err)
	}
}

func TestTypedScalarsAndSlices(t *testing.T) {
	typedRoundTrip(t, "строка")
	typedRoundTrip(t, "")
	typedRoundTrip(t, true)
	typedRoundTrip(t, false)
	typedRoundTrip(t, int(-123))
	typedRoundTrip(t, int8(math.MinInt8))
	typedRoundTrip(t, int16(math.MinInt16))
	typedRoundTrip(t, int32(math.MinInt32))
	typedRoundTrip(t, int64(math.MinInt64))
	typedRoundTrip(t, uint(123))
	typedRoundTrip(t, uint8(math.MaxUint8))
	typedRoundTrip(t, uint16(math.MaxUint16))
	typedRoundTrip(t, uint32(math.MaxUint32))
	typedRoundTrip(t, uint64(math.MaxUint64))
	typedRoundTrip(t, float32(math.MaxFloat32))
	typedRoundTrip(t, float64(math.MaxFloat64))
	typedRoundTrip(t, math.SmallestNonzeroFloat64)
	typedRoundTrip(t, []bool{false, true})
	typedRoundTrip(t, []string{"a", "", "б"})
	typedRoundTrip(t, []int{-1, 0, 1})
	typedRoundTrip(t, []int8{math.MinInt8, math.MaxInt8})
	typedRoundTrip(t, []int16{math.MinInt16, math.MaxInt16})
	typedRoundTrip(t, []int32{math.MinInt32, math.MaxInt32})
	typedRoundTrip(t, []int64{math.MinInt64, math.MaxInt64})
	typedRoundTrip(t, []uint{0, 123})
	typedRoundTrip(t, []uint8{0, 255, 0})
	typedRoundTrip(t, []uint16{0, math.MaxUint16})
	typedRoundTrip(t, []uint32{0, math.MaxUint32})
	typedRoundTrip(t, []uint64{0, math.MaxUint64})
	typedRoundTrip(t, []float32{0.5, math.MaxFloat32})
	typedRoundTrip(t, []float64{0.5, math.SmallestNonzeroFloat64})
	typedRoundTrip(t, []string(nil))
	typedRoundTrip(t, []string{})
	typedRoundTrip(t, []byte(nil))
	typedRoundTrip(t, []byte{})
	typedRoundTrip(t, timeout(-100))
	typedRoundTrip(t, names{"one", "two"})
}

func TestTypedViewsShareStoreAndPreserveOtherKeys(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	must(t, Typed[string](s).Set("name", "ESP32"))
	must(t, Typed[int](s).Set("count", 42))
	must(t, Typed[bool](s).Set("enabled", true))
	input := []uint16{80, 443}
	must(t, Typed[[]uint16](s).Set("ports", input))
	input[0] = 1
	s = openTest(t, f, 0, DefaultSize)
	name, found, err := Typed[string](s).Get("name")
	must(t, err)
	if !found || name != "ESP32" {
		t.Fatal(name, found)
	}
	count, found, err := Typed[int](s).Get("count")
	must(t, err)
	if !found || count != 42 {
		t.Fatal(count, found)
	}
	enabled, found, err := Typed[bool](s).Get("enabled")
	must(t, err)
	if !found || !enabled {
		t.Fatal(enabled, found)
	}
	ports, found, err := Typed[[]uint16](s).Get("ports")
	must(t, err)
	if !found || !reflect.DeepEqual(ports, []uint16{80, 443}) {
		t.Fatal(ports, found)
	}
	ports[0] = 1
	ports, _, err = Typed[[]uint16](s).Get("ports")
	must(t, err)
	if ports[0] != 80 {
		t.Fatal("returned slice aliases stored value")
	}
	must(t, Typed[int](s).Set("count", 43))
	must(t, s.Delete("enabled"))
	s = openTest(t, f, 0, DefaultSize)
	count, found, err = Typed[int](s).Get("count")
	must(t, err)
	if !found || count != 43 {
		t.Fatal(count, found)
	}
	if _, found, err := Typed[bool](s).Get("enabled"); found || err != nil {
		t.Fatal(found, err)
	}
}

func TestTypedErrorsDoNotModifyStorage(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	must(t, Typed[int](s).Set("number", 42))
	before := bytes.Clone(f.data)
	if v, found, err := Typed[string](s).Get("number"); v != "" || found || !errors.Is(err, ErrTypeMismatch) {
		t.Fatal(v, found, err)
	}
	if err := Typed[float64](s).Set("number", math.NaN()); err == nil {
		t.Fatal("NaN must fail serialization")
	}
	if err := Typed[float64](s).Set("number", math.Inf(1)); err == nil {
		t.Fatal("infinity must fail serialization")
	}
	if !bytes.Equal(before, f.data) {
		t.Fatal("type or serialization error modified flash")
	}
	if _, found, err := Typed[int](s).Get(""); found || err != ErrInvalidKey {
		t.Fatal(found, err)
	}
	if err := Typed[int](s).Set("", 1); err != ErrInvalidKey {
		t.Fatal(err)
	}
	// A new type at an existing key must replace the tag too.
	must(t, Typed[bool](s).Set("number", false))
	s = openTest(t, f, 0, DefaultSize)
	if _, found, err := Typed[int](s).Get("number"); found || err != ErrTypeMismatch {
		t.Fatal(found, err)
	}
	if v, found, err := Typed[bool](s).Get("number"); v || !found || err != nil {
		t.Fatal(v, found, err)
	}
}

func TestTypedLegacyValuesAndStringHelpers(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	must(t, s.Set("old", []byte("saved before serialization")))
	s = openTest(t, f, 0, DefaultSize)
	v, found, err := Typed[string](s).Get("old")
	must(t, err)
	if !found || v != "saved before serialization" {
		t.Fatal(v, found)
	}
	b, found, err := Typed[[]byte](s).Get("old")
	must(t, err)
	if !found || string(b) != v {
		t.Fatal(b, found)
	}
	if _, found, err := Typed[int](s).Get("old"); found || err != ErrTypeMismatch {
		t.Fatal(found, err)
	}
	must(t, s.SetString("new", "helper"))
	v, found, err = Typed[string](s).Get("new")
	must(t, err)
	if !found || v != "helper" {
		t.Fatal(v, found)
	}
	must(t, Typed[string](s).Set("new", "typed"))
	v, found, err = s.GetString("new")
	must(t, err)
	if !found || v != "typed" {
		t.Fatal(v, found)
	}
}
