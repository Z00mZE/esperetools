package kvstorage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var errPowerLoss = errors.New("power lost")

// Model NOR flash, including alignment, clearing-only writes, and partial
// sector erases. A reset discards the Store and reopens the same flash bytes.
type memoryFlash struct {
	data            []byte
	mutations       int
	failAt, partial int
	failVerify      bool
}

func newFlash(blocks int) *memoryFlash {
	return &memoryFlash{data: bytes.Repeat([]byte{255}, blocks*4096), failAt: -1}
}

func (f *memoryFlash) Size() int64           { return int64(len(f.data)) }
func (f *memoryFlash) EraseBlockSize() int64 { return 4096 }
func (f *memoryFlash) WriteBlockSize() int64 { return 4 }
func (f *memoryFlash) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 || off > f.Size() || int64(len(b)) > f.Size()-off {
		panic("read out of bounds")
	}
	if f.failVerify {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(b, f.data[off:]), nil
}
func (f *memoryFlash) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 || off%4 != 0 || len(b)%4 != 0 || off > f.Size() || int64(len(b)) > f.Size()-off {
		panic("invalid write")
	}
	fail := f.mutations == f.failAt
	f.mutations++
	n := len(b)
	if fail && f.partial < n {
		n = f.partial
	}
	for i, v := range b[:n] {
		if f.data[int(off)+i]&v != v {
			panic("write attempted 0 -> 1")
		}
		f.data[int(off)+i] &= v
	}
	if fail {
		return n, errPowerLoss
	}
	return n, nil
}
func (f *memoryFlash) EraseBlocks(start, count int64) error {
	if start < 0 || count < 0 || start > f.Size()/4096 || count > f.Size()/4096-start {
		panic("invalid erase")
	}
	fail := f.mutations == f.failAt
	f.mutations++
	n := int(count * 4096)
	if fail && f.partial < n {
		n = f.partial
	}
	for i := 0; i < n; i++ {
		f.data[int(start*4096)+i] = 255
	}
	if fail {
		return errPowerLoss
	}
	return nil
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func openTest(t *testing.T, f Flash, offset, size int64) *Store {
	t.Helper()
	s, err := NewStore(f, offset, size)
	must(t, err)
	return s
}

func TestValuesSurvivePowerCycle(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	if f.mutations != 0 {
		t.Fatal("opening blank flash wrote data")
	}
	must(t, s.Set[string]("ssid", "Домашняя сеть"))
	source := []byte{0, 1, 255, 0}
	must(t, s.SetBytes("binary", source))
	source[0] = 99
	must(t, s.SetBytes("empty", nil))
	must(t, s.Set[string]("ключ", "значение"))
	// Drop all cached state to model turning power off, then back on.
	s = openTest(t, f, 0, DefaultSize)
	ssid, _, err := s.Get[string]("ssid")
	must(t, err)
	if ssid != "Домашняя сеть" {
		t.Fatal(ssid)
	}
	b, _, err := s.GetBytes("binary")
	must(t, err)
	if !bytes.Equal(b, []byte{0, 1, 255, 0}) {
		t.Fatal(b)
	}
	b[0] = 42
	b, _, err = s.GetBytes("binary")
	must(t, err)
	if b[0] != 0 {
		t.Fatal("Get returned internal memory")
	}
	b, _, err = s.GetBytes("empty")
	must(t, err)
	if len(b) != 0 {
		t.Fatal(b)
	}
	v, _, err := s.Get[string]("ключ")
	must(t, err)
	if v != "значение" {
		t.Fatal(v)
	}
	has, err := s.Has("empty")
	must(t, err)
	if !has {
		t.Fatal("empty value confused with missing key")
	}
}

func TestUpdateDeleteClearAndNoOp(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	must(t, s.Set[string]("a", "old"))
	must(t, s.Set[string]("a", "new"))
	must(t, s.Set[string]("b", "keep"))
	n := f.mutations
	must(t, s.Set[string]("a", "new"))
	if f.mutations != n {
		t.Fatal("identical Set erased flash")
	}
	keys, err := s.Keys()
	must(t, err)
	if !reflect.DeepEqual(keys, []string{"a", "b"}) {
		t.Fatal(keys)
	}
	must(t, s.Delete("a"))
	s = openTest(t, f, 0, DefaultSize)
	if _, found, err := s.GetBytes("a"); err != nil || found {
		t.Fatalf("found=%v, err=%v", found, err)
	}
	v, _, err := s.Get[string]("b")
	must(t, err)
	if v != "keep" {
		t.Fatal(v)
	}
	if err := s.Delete("a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	must(t, s.Clear())
	s = openTest(t, f, 0, DefaultSize)
	keys, err = s.Keys()
	must(t, err)
	if len(keys) != 0 {
		t.Fatal(keys)
	}
	n = f.mutations
	must(t, s.Clear())
	if f.mutations != n {
		t.Fatal("empty Clear erased flash")
	}
}

func TestCapacityAndInvalidKeysDoNotDamageData(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	// Four count bytes + six record bytes + one key byte = 11 bytes.
	value := bytes.Repeat([]byte{42}, 4096-headerSize-11)
	must(t, s.SetBytes("k", value))
	n := f.mutations
	if err := s.SetBytes("k", append(value, 1)); err != ErrFull {
		t.Fatal(err)
	}
	if err := s.Set[string]("extra", "x"); err != ErrFull {
		t.Fatal(err)
	}
	for _, key := range []string{"", strings.Repeat("x", MaxKeySize+1)} {
		if err := s.Set[string](key, "x"); err != ErrInvalidKey {
			t.Fatal(err)
		}
		if _, _, err := s.GetBytes(key); err != ErrInvalidKey {
			t.Fatal(err)
		}
		if err := s.Delete(key); err != ErrInvalidKey {
			t.Fatal(err)
		}
	}
	if f.mutations != n {
		t.Fatal("invalid mutation erased flash")
	}
	s = openTest(t, f, 0, DefaultSize)
	got, _, err := s.GetBytes("k")
	must(t, err)
	if !bytes.Equal(got, value) {
		t.Fatal("full store lost value")
	}
	must(t, s.Delete("k"))
	must(t, s.Set[string]("extra", "fits now"))
}

func TestRegionAndLargerBanks(t *testing.T) {
	f := newFlash(6)
	copy(f.data[:4096], bytes.Repeat([]byte{0x55}, 4096))
	copy(f.data[5*4096:], bytes.Repeat([]byte{0xaa}, 4096))
	s := openTest(t, f, 4096, 4*4096)
	value := bytes.Repeat([]byte{123}, 7000)
	for i := 0; i < 8; i++ {
		value[0] = byte(i)
		must(t, s.SetBytes("large", value))
	}
	s = openTest(t, f, 4096, 4*4096)
	got, _, err := s.GetBytes("large")
	must(t, err)
	if !bytes.Equal(got, value) {
		t.Fatal("large value changed")
	}
	if !bytes.Equal(f.data[:4096], bytes.Repeat([]byte{0x55}, 4096)) || !bytes.Equal(f.data[5*4096:], bytes.Repeat([]byte{0xaa}, 4096)) {
		t.Fatal("modified bytes outside reserved region")
	}
	for _, tc := range [][2]int64{{-1, 8192}, {1, 8192}, {0, 4096}, {0, 12288}, {5 * 4096, 8192}, {0, -8192}} {
		if _, err := NewStore(f, tc[0], tc[1]); err != ErrInvalidFlash {
			t.Fatal(tc, err)
		}
	}
	if _, err := NewStore(nil, 0, 8192); err != ErrInvalidFlash {
		t.Fatal(err)
	}
}

func TestCorruptionRecoveryAndExplicitFormat(t *testing.T) {
	for _, damage := range []int{0, 12, 16, 24, 28, headerSize + 10} {
		t.Run(fmt.Sprint(damage), func(t *testing.T) {
			f := newFlash(4)
			s := openTest(t, f, 4096, DefaultSize)
			must(t, s.Set[string]("key", "old"))
			must(t, s.Set[string]("key", "new"))
			f.data[4096+s.active*4096+damage] ^= 1
			s = openTest(t, f, 4096, DefaultSize)
			v, _, err := s.Get[string]("key")
			must(t, err)
			if v != "old" {
				t.Fatal("did not recover previous snapshot", v)
			}
		})
	}
	f := newFlash(4)
	s := openTest(t, f, 4096, DefaultSize)
	must(t, s.Set[string]("key", "old"))
	must(t, s.Set[string]("key", "new"))
	f.data[4096+24] ^= 1
	f.data[8192+24] ^= 1
	before := bytes.Clone(f.data)
	if _, err := NewStore(f, 4096, DefaultSize); err != ErrCorrupt {
		t.Fatal(err)
	}
	if !bytes.Equal(f.data, before) {
		t.Fatal("corrupt store was auto-formatted")
	}
	s, err := Format(f, 4096, DefaultSize)
	must(t, err)
	if !bytes.Equal(f.data[:4096], before[:4096]) || !bytes.Equal(f.data[12288:], before[12288:]) {
		t.Fatal("Format erased other regions")
	}
	must(t, s.Set[string]("key", "fresh"))
	s = openTest(t, f, 4096, DefaultSize)
	v, _, err := s.Get[string]("key")
	must(t, err)
	if v != "fresh" {
		t.Fatal(v)
	}
}

func TestFirstUseOfUnformattedRegion(t *testing.T) {
	for _, fill := range []byte{0xff, 0x00, 0x55, 0xfe} {
		t.Run(fmt.Sprintf("fill_%02x", fill), func(t *testing.T) {
			f := newFlash(4)
			copy(f.data[4096:12288], bytes.Repeat([]byte{fill}, 8192))
			before := bytes.Clone(f.data)
			s := openTest(t, f, 4096, DefaultSize)
			if _, found, err := s.GetBytes("key"); err != nil || found {
				t.Fatalf("found=%v, err=%v", found, err)
			}
			if f.mutations != 0 {
				t.Fatal("Open must initialize lazily, on first write")
			}
			must(t, s.Set[string]("key", "first"))
			s = openTest(t, f, 4096, DefaultSize)
			v, _, err := s.Get[string]("key")
			must(t, err)
			if v != "first" {
				t.Fatal(v)
			}
			must(t, s.Set[string]("key", "replacement"))
			s = openTest(t, f, 4096, DefaultSize)
			v, _, err = s.Get[string]("key")
			must(t, err)
			if v != "replacement" {
				t.Fatal(v)
			}
			must(t, s.Delete("key"))
			s = openTest(t, f, 4096, DefaultSize)
			if _, found, err := s.GetBytes("key"); err != nil || found {
				t.Fatalf("found=%v, err=%v", found, err)
			}
			if !bytes.Equal(f.data[:4096], before[:4096]) || !bytes.Equal(f.data[12288:], before[12288:]) {
				t.Fatal("initialization modified another region")
			}
		})
	}
}

func TestDamagedCommittedSnapshotIsNotFirstUse(t *testing.T) {
	for _, damage := range []int{0, 24, commitOffset, headerSize + 10} {
		t.Run(fmt.Sprint(damage), func(t *testing.T) {
			f := newFlash(2)
			s := openTest(t, f, 0, DefaultSize)
			must(t, s.Set[string]("key", "saved"))
			// Clear a set marker bit, or flip a data/header bit.
			f.data[damage] ^= 1
			before := bytes.Clone(f.data)
			if _, err := NewStore(f, 0, DefaultSize); err != ErrCorrupt {
				t.Fatal(err)
			}
			if !bytes.Equal(f.data, before) {
				t.Fatal("damaged saved data was erased")
			}
		})
	}
}

func TestUnsupportedVersionNeverAutoFormats(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	must(t, s.Set[string]("key", "x"))
	binary.LittleEndian.PutUint32(f.data[8:], 2)
	binary.LittleEndian.PutUint32(f.data[28:], crc32.ChecksumIEEE(f.data[:28]))
	before := bytes.Clone(f.data)
	if _, err := NewStore(f, 0, DefaultSize); err != ErrUnsupported {
		t.Fatal(err)
	}
	if !bytes.Equal(f.data, before) {
		t.Fatal("unsupported store changed")
	}
}

func snapshot(t *testing.T, s *Store) map[string]string {
	t.Helper()
	keys, err := s.Keys()
	must(t, err)
	result := make(map[string]string)
	for _, k := range keys {
		v, _, err := s.Get[string](k)
		must(t, err)
		result[k] = v
	}
	return result
}

func TestPowerLossDuringEveryMutation(t *testing.T) {
	for _, scenario := range []string{"first", "first-unformatted", "update", "delete", "clear"} {
		t.Run(scenario, func(t *testing.T) {
			base := newFlash(2)
			if scenario == "first-unformatted" {
				copy(base.data, bytes.Repeat([]byte{0}, len(base.data)))
			}
			s := openTest(t, base, 0, DefaultSize)
			if scenario != "first" && scenario != "first-unformatted" {
				must(t, s.Set[string]("guard", "keep"))
				must(t, s.Set[string]("value", strings.Repeat("o", 500)))
				// Both banks now contain a committed snapshot; the next write
				// must survive a partial erase of the inactive older bank.
			}
			old := snapshot(t, s)
			data := bytes.Clone(base.data)
			action := func(s *Store) error {
				switch scenario {
				case "delete":
					return s.Delete("value")
				case "clear":
					return s.Clear()
				default:
					return s.Set[string]("value", strings.Repeat("n", 700))
				}
			}
			probe := &memoryFlash{data: bytes.Clone(data), failAt: -1}
			ps := openTest(t, probe, 0, DefaultSize)
			must(t, action(ps))
			want := snapshot(t, ps)
			for cut := 0; cut < probe.mutations; cut++ {
				for _, partial := range []int{0, 1, 2, 3, 4, 7, 8, 15, 31, 32, 33, 63, 64, 255, 4095, 10000} {
					t.Run(fmt.Sprintf("%d_%d", cut, partial), func(t *testing.T) {
						f := &memoryFlash{data: bytes.Clone(data), failAt: -1}
						handle := openTest(t, f, 0, DefaultSize)
						f.failAt, f.partial = cut, partial
						if err := action(handle); !errors.Is(err, errPowerLoss) {
							t.Fatal(err)
						}
						if _, _, err := handle.GetBytes("value"); err != ErrNeedsReopen {
							t.Fatal(err)
						}
						if err := handle.Set[string]("other", "x"); err != ErrNeedsReopen {
							t.Fatal(err)
						}
						f.failAt = -1
						// No ErrCorrupt exceptions: every modeled interruption
						// must recover a complete old or new snapshot.
						rebooted := openTest(t, f, 0, DefaultSize)
						got := snapshot(t, rebooted)
						if !reflect.DeepEqual(got, old) && !reflect.DeepEqual(got, want) {
							t.Fatal("mixed snapshot", got)
						}
						must(t, rebooted.Set[string]("after", "recovered"))
						rebooted = openTest(t, f, 0, DefaultSize)
						v, _, err := rebooted.Get[string]("after")
						must(t, err)
						if v != "recovered" {
							t.Fatal(v)
						}
					})
				}
			}
		})
	}
}

// Simulate a read failure after a fully committed write. The old handle must
// stop, and reopening must observe the value which did reach flash.
type verifyFailureFlash struct {
	*memoryFlash
	fail bool
}

func (f *verifyFailureFlash) WriteAt(b []byte, off int64) (int, error) {
	n, err := f.memoryFlash.WriteAt(b, off)
	if err == nil && off%4096 == commitOffset && f.fail {
		f.failVerify = true
	}
	return n, err
}

func TestVerificationFailureRequiresReopen(t *testing.T) {
	f := &verifyFailureFlash{memoryFlash: newFlash(2)}
	s := openTest(t, f, 0, DefaultSize)
	must(t, s.Set[string]("key", "old"))
	f.fail = true
	if err := s.Set[string]("key", "new"); err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
	if _, _, err := s.GetBytes("key"); err != ErrNeedsReopen {
		t.Fatal(err)
	}
	f.fail, f.failVerify = false, false
	s = openTest(t, f, 0, DefaultSize)
	v, _, err := s.Get[string]("key")
	must(t, err)
	if v != "new" {
		t.Fatal(v)
	}
}

func TestConcurrentUse(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprint(i)
			for n := 0; n < 30; n++ {
				if err := s.Set[string](key, fmt.Sprint(n)); err != nil {
					t.Error(err)
					return
				}
				if _, _, err := s.GetBytes(key); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	s = openTest(t, f, 0, DefaultSize)
	for i := 0; i < 4; i++ {
		v, _, err := s.Get[string](fmt.Sprint(i))
		must(t, err)
		if v != "29" {
			t.Fatal(v)
		}
	}
}

func TestGetterFoundAndErrors(t *testing.T) {
	f := newFlash(2)
	s := openTest(t, f, 0, DefaultSize)
	if v, found, err := s.GetBytes("missing"); v != nil || found || err != nil {
		t.Fatalf("%v %v %v", v, found, err)
	}
	if v, found, err := s.Get[string]("missing"); v != "" || found || err != nil {
		t.Fatalf("%q %v %v", v, found, err)
	}
	must(t, s.SetBytes("empty", nil))
	must(t, s.Set[string]("name", "ESP32"))
	s = openTest(t, f, 0, DefaultSize)
	if v, found, err := s.GetBytes("empty"); len(v) != 0 || !found || err != nil {
		t.Fatalf("%v %v %v", v, found, err)
	}
	if v, found, err := s.Get[string]("empty"); v != "" || !found || err != nil {
		t.Fatalf("%q %v %v", v, found, err)
	}
	if v, found, err := s.Get[string]("name"); v != "ESP32" || !found || err != nil {
		t.Fatalf("%q %v %v", v, found, err)
	}
	if v, found, err := s.GetBytes(""); v != nil || found || err != ErrInvalidKey {
		t.Fatalf("%v %v %v", v, found, err)
	}
	if v, found, err := s.Get[string](""); v != "" || found || err != ErrInvalidKey {
		t.Fatalf("%q %v %v", v, found, err)
	}
	f.failAt = f.mutations
	if err := s.Set[string]("name", "new"); err != errPowerLoss {
		t.Fatal(err)
	}
	if v, found, err := s.GetBytes("name"); v != nil || found || err != ErrNeedsReopen {
		t.Fatalf("%v %v %v", v, found, err)
	}
	if v, found, err := s.Get[string]("name"); v != "" || found || err != ErrNeedsReopen {
		t.Fatalf("%q %v %v", v, found, err)
	}
}
