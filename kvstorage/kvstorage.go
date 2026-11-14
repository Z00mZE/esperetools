// Package kvstorage stores key/value data in NOR flash. Set and Delete are
// synchronous: a successful return means the new snapshot has been persisted
// and read back. Two alternating flash banks retain the previous snapshot
// until the replacement is committed. No ESP-IDF or NVS libraries are needed.
package kvstorage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"sort"
	"sync"
	"reflect"
)

const (
	// DefaultSize reserves two 4 KiB sectors: one current bank and one spare.
	DefaultSize   int64  = 8192
	MaxKeySize           = 255
	headerSize           = 64
	commitOffset         = 32
	commitMarker  uint32 = 0x4b564f4b
	formatVersion uint32 = 1

	corrupt bankState = iota
	uninitialized
	valid

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

var (
	signature = [8]byte{'K', 'V', 'S', 'T', 'O', 'R', 'E', '1'}
)

var (
	ErrNotFound     = errors.New("kvstorage: key not found")
	ErrTypeMismatch = errors.New("kvstorage: value has a different type")
	ErrInvalidKey   = errors.New("kvstorage: key must contain 1..255 bytes")
	ErrFull         = errors.New("kvstorage: data does not fit in a bank")
	ErrInvalidFlash = errors.New("kvstorage: invalid flash region or block sizes")
	ErrCorrupt      = errors.New("kvstorage: no valid committed snapshot")
	ErrUnsupported  = errors.New("kvstorage: unsupported storage version")
	ErrVerify       = errors.New("kvstorage: flash verification failed")
	// A failed mutation may have reached flash. Reopen before using the store.
	ErrNeedsReopen        = errors.New("kvstorage: reopen after failed flash operation")
	ErrGenerationOverflow = errors.New("kvstorage: generation counter exhausted")
)

// The envelope lives inside the existing byte-value format, so old snapshots
// remain readable. The last byte is the serialization version; the next byte
// is a stable scalar type code, with bit 7 set for slices.
var valueMagic = [8]byte{0, 'K', 'V', 'J', 'S', 'O', 'N', 1}

var scalarKinds = [...]byte{
	reflect.Bool:    kindBool,
	reflect.String:  kindString,
	reflect.Int:     kindInt,
	reflect.Int8:    kindInt8,
	reflect.Int16:   kindInt16,
	reflect.Int32:   kindInt32,
	reflect.Int64:   kindInt64,
	reflect.Uint:    kindUint,
	reflect.Uint8:   kindUint8,
	reflect.Uint16:  kindUint16,
	reflect.Uint32:  kindUint32,
	reflect.Uint64:  kindUint64,
	reflect.Float32: kindFloat32,
	reflect.Float64: kindFloat64,
}


type bankState byte

type bank struct {
	index      int
	state      bankState
	generation uint64
	payload    []byte
	values     map[string][]byte
}

// Value supports scalars and slices of built-in scalar types. byte and rune
// are aliases of uint8 and int32. Nested slices, pointers and structs are excluded.
type Value interface {
	~bool | ~string | ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint |
		~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64 | ~[]bool |
		~[]string | ~[]int | ~[]int8 | ~[]int16 | ~[]int32 | ~[]int64 | ~[]uint |
		~[]uint8 | ~[]uint16 | ~[]uint32 | ~[]uint64 | ~[]float32 | ~[]float64
}


// Flash is implemented by machine.Flash on ESP32-C3/S3 in TinyGo 0.44.
// Offsets are bytes; EraseBlocks arguments are block indexes and counts.
// Methods must finish the hardware operation before returning. Writes may only
// clear bits from 1 to 0, and must not modify bytes outside the supplied buffer.
type Flash interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	EraseBlocks(start, count int64) error
	Size() int64
	EraseBlockSize() int64
	WriteBlockSize() int64
}

// Store owns a reserved flash region. Use one Store per region. Its methods
// are safe for goroutines, but must not be called from interrupt handlers.
// Do not copy a Store. Values are cached in RAM and persisted on every mutation.
type Store struct {
	mu               sync.Mutex
	flash            Flash
	offset, bankSize int64
	active           int
	generation       uint64
	values           map[string][]byte
	fault            bool
}

// NewStore opens a reserved flash region. size must contain two equal banks, each
// aligned to erase blocks and at least 4096 bytes. A region without an existing
// kvstorage snapshot opens empty, even if it contains bytes from old firmware.
// The first Set initializes it. Existing corrupt snapshots are not formatted.
// Reopen with the same physical region after reset to recover stored values.
func NewStore(f Flash, offset, size int64) (*Store, error) {
	if err := validateFlash(f, offset, size); err != nil {
		return nil, err
	}

	s := &Store{flash: f, offset: offset, bankSize: size / 2, active: -1, values: make(map[string][]byte)}

	a, err := s.readBank(0)
	if err != nil {
		return nil, err
	}
	b, err := s.readBank(1)
	if err != nil {
		return nil, err
	}
	if a.state == valid && b.state == valid && a.generation == b.generation && !bytes.Equal(a.payload, b.payload) {
		return nil, ErrCorrupt
	}
	chosen := a
	if b.state == valid && (a.state != valid || b.generation > a.generation) {
		chosen = b
	}
	if chosen.state == valid {
		s.active, s.generation, s.values = chosen.index, chosen.generation, chosen.values
		return s, nil
	}
	// A reserved region need not have been erased by the firmware flasher.
	// Without signs of a committed kvstorage snapshot, this is first use:
	// opening is read-only, and the first Set initializes an inactive bank.
	if a.state == uninitialized && b.state == uninitialized {
		return s, nil
	}
	return nil, ErrCorrupt
}

func validateFlash(f Flash, offset, size int64) error {
	if f == nil {
		return ErrInvalidFlash
	}
	erase, write := f.EraseBlockSize(), f.WriteBlockSize()
	if erase < 4096 || erase%4 != 0 || (write != 1 && write != 2 && write != 4) ||
		offset < 0 || size/2 < erase || offset%erase != 0 || size%2 != 0 || (size/2)%erase != 0 ||
		offset > f.Size() || size > f.Size()-offset || uint64(size/2) > uint64(^uint32(0)) || uint64(size/2) > uint64(^uint(0)>>1) {
		return ErrInvalidFlash
	}
	return nil
}

// Format explicitly erases ONLY the specified region and opens an empty store.
// All keys in that region are lost. Normal opening and writes never call Format.
func Format(f Flash, offset, size int64) (*Store, error) {
	if err := validateFlash(f, offset, size); err != nil {
		return nil, err
	}
	if err := f.EraseBlocks(offset/f.EraseBlockSize(), size/f.EraseBlockSize()); err != nil {
		return nil, err
	}
	return NewStore(f, offset, size)
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
	return s.setBytes(key, b)
}

// Get returns the typed value, its presence, and an error. A missing key
// returns the zero value, false, nil. On an error it returns zero, false, err.
// A different stored scalar type (including slice element type) returns
// ErrTypeMismatch rather than silently converting or losing numeric precision.
func (s *Store) Get[T Value](key string) (T, bool, error) {
	var value T
	b, found, err := s.getBytes(key)
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
		b = b[len(valueMagic)+1:]
	} else {
		// Earlier snapshots have untyped bytes. Preserve string and []byte
		// reads, but do not guess a numeric or boolean type.
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
	}
	if err := json.Unmarshal(b, &value); err != nil {
		var zero T
		return zero, false, err
	}
	return value, true, nil
}

// Delete persists removal of a key. A missing key returns ErrNotFound.
func (s *Store) Delete(key string) error {
	if !validKey(key) {
		return ErrInvalidKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault {
		return ErrNeedsReopen
	}
	if _, ok := s.values[key]; !ok {
		return ErrNotFound
	}
	values := s.copyValues()
	delete(values, key)
	return s.commit(values)
}

func (s *Store) Has(key string) (bool, error) {
	if !validKey(key) {
		return false, ErrInvalidKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault {
		return false, ErrNeedsReopen
	}
	_, ok := s.values[key]
	return ok, nil
}

// Keys returns a sorted snapshot of the keys.
func (s *Store) Keys() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault {
		return nil, ErrNeedsReopen
	}
	keys := make([]string, 0, len(s.values))
	for k := range s.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// Clear persists an empty store, retaining the same power-loss guarantees as Set.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault {
		return ErrNeedsReopen
	}
	if len(s.values) == 0 {
		return nil
	}
	return s.commit(make(map[string][]byte))
}

func (s *Store) readBank(index int) (bank, error) {
	r := bank{index: index, state: corrupt}
	b := make([]byte, int(s.bankSize))
	if err := readFull(s.flash, b, s.offset+int64(index)*s.bankSize); err != nil {
		return r, err
	}
	marker := binary.LittleEndian.Uint32(b[commitOffset:])
	ownHeader := partialSignature(b)
	if !ownHeader && marker != commitMarker {
		r.state = uninitialized
		return r, nil
	}
	if marker != commitMarker {
		// An interrupted NOR write can only leave some intended zero bits
		// as ones. Cleared bits which belong to the marker indicate damage.
		if ownHeader && marker&commitMarker == commitMarker {
			r.state = uninitialized
		}
		return r, nil
	}
	if !bytes.Equal(b[:8], signature[:]) || crc32.ChecksumIEEE(b[:28]) != binary.LittleEndian.Uint32(b[28:]) {
		return r, nil
	}
	if binary.LittleEndian.Uint32(b[8:]) != formatVersion {
		return r, ErrUnsupported
	}
	length := int64(binary.LittleEndian.Uint32(b[12:]))
	r.generation = binary.LittleEndian.Uint64(b[16:])
	if length < 4 || length > s.bankSize-headerSize || r.generation == 0 {
		return r, nil
	}
	r.payload = b[headerSize : headerSize+int(length)]
	if crc32.ChecksumIEEE(r.payload) != binary.LittleEndian.Uint32(b[24:]) {
		return r, nil
	}
	values, err := decodeSnapshot(r.payload)
	if err != nil {
		return r, nil
	}
	r.state, r.values = valid, values
	return r, nil
}

func (s *Store) write(b []byte, off int64) error {
	n, err := s.flash.WriteAt(b, off)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

func (s *Store) commit(values map[string][]byte) (err error) {
	payload, err := encodeSnapshot(values, s.bankSize-headerSize)
	if err != nil {
		return err
	}
	if s.generation == ^uint64(0) {
		return ErrGenerationOverflow
	}
	next := s.generation + 1
	target := 0
	if s.active == 0 {
		target = 1
	}
	off := s.offset + int64(target)*s.bankSize
	eraseSize := s.flash.EraseBlockSize()
	// From this point on a failure may leave a newer snapshot on flash. Block
	// this handle until reopened, so its cached state cannot overwrite it.
	defer func() {
		if err != nil {
			s.fault = true
		}
	}()
	if err = s.flash.EraseBlocks(off/eraseSize, s.bankSize/eraseSize); err != nil {
		return err
	}
	var header [32]byte
	copy(header[:], signature[:])
	binary.LittleEndian.PutUint32(header[8:], formatVersion)
	binary.LittleEndian.PutUint32(header[12:], uint32(len(payload)))
	binary.LittleEndian.PutUint64(header[16:], next)
	binary.LittleEndian.PutUint32(header[24:], crc32.ChecksumIEEE(payload))
	binary.LittleEndian.PutUint32(header[28:], crc32.ChecksumIEEE(header[:28]))
	if err = s.write(header[:], off); err != nil {
		return err
	}
	aligned := make([]byte, (len(payload)+3)&^3)
	for i := range aligned {
		aligned[i] = 255
	}
	copy(aligned, payload)
	if err = s.write(aligned, off+headerSize); err != nil {
		return err
	}
	// The commit word is programmed LAST. The previous bank stays untouched.
	var marker [4]byte
	binary.LittleEndian.PutUint32(marker[:], commitMarker)
	if err = s.write(marker[:], off+commitOffset); err != nil {
		return err
	}
	var saved bank
	saved, err = s.readBank(target)
	if err != nil {
		return err
	}
	if saved.state != valid || saved.generation != next || !bytes.Equal(saved.payload, payload) {
		return ErrVerify
	}
	s.active, s.generation, s.values = target, next, values
	return nil
}

// setBytes persists the serialized value. The caller retains its buffer.
func (s *Store) setBytes(key string, value []byte) error {
	if !validKey(key) {
		return ErrInvalidKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault {
		return ErrNeedsReopen
	}
	// Reject an oversized value before copying the caller's buffer into RAM.
	if int64(len(value)) > s.bankSize-headerSize-4-6-int64(len(key)) {
		return ErrFull
	}
	if old, ok := s.values[key]; ok && bytes.Equal(old, value) {
		return nil
	}
	values := s.copyValues()
	values[key] = append([]byte{}, value...)
	return s.commit(values)
}

func (s *Store) getBytes(key string) ([]byte, bool, error) {
	if !validKey(key) {
		return nil, false, ErrInvalidKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault {
		return nil, false, ErrNeedsReopen
	}
	value, ok := s.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte{}, value...), true, nil
}

// copyValues is called while mu is held. Serialized values are immutable, so
// copying the map is sufficient; each mutation supplies a new value buffer.
func (s *Store) copyValues() map[string][]byte {
	values := make(map[string][]byte, len(s.values))
	for k, v := range s.values {
		values[k] = v
	}
	return values
}

func decodeSnapshot(b []byte) (map[string][]byte, error) {
	if len(b) < 4 {
		return nil, ErrCorrupt
	}
	count := uint64(binary.LittleEndian.Uint32(b))
	b = b[4:]
	if count > uint64(len(b)/7) {
		return nil, ErrCorrupt
	}
	values := make(map[string][]byte, int(count))
	for i := uint64(0); i < count; i++ {
		if len(b) < 6 {
			return nil, ErrCorrupt
		}
		keyLen, valueLen := int(binary.LittleEndian.Uint16(b)), uint64(binary.LittleEndian.Uint32(b[2:]))
		b = b[6:]
		if keyLen < 1 || keyLen > MaxKeySize || uint64(keyLen)+valueLen > uint64(len(b)) {
			return nil, ErrCorrupt
		}
		key := string(b[:keyLen])
		if _, exists := values[key]; exists {
			return nil, ErrCorrupt
		}
		values[key] = append([]byte{}, b[keyLen:keyLen+int(valueLen)]...)
		b = b[keyLen+int(valueLen):]
	}
	if len(b) != 0 {
		return nil, ErrCorrupt
	}
	return values, nil
}

func encodeSnapshot(values map[string][]byte, limit int64) ([]byte, error) {
	length := int64(4)
	keys := make([]string, 0, len(values))
	for k, v := range values {
		length += 6 + int64(len(k)) + int64(len(v))
		if length > limit {
			return nil, ErrFull
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := make([]byte, int(length))
	binary.LittleEndian.PutUint32(b, uint32(len(keys)))
	pos := 4
	for _, k := range keys {
		v := values[k]
		binary.LittleEndian.PutUint16(b[pos:], uint16(len(k)))
		binary.LittleEndian.PutUint32(b[pos+2:], uint32(len(v)))
		pos += 6
		pos += copy(b[pos:], k)
		pos += copy(b[pos:], v)
	}
	return b, nil
}

func readFull(f Flash, b []byte, off int64) error {
	n, err := f.ReadAt(b, off)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func allErased(b []byte) bool {
	for _, v := range b {
		if v != 255 {
			return false
		}
	}
	return true
}

// Header programming is the first write to an erased bank. A prefix of its
// signature is enough to recognize an interrupted first header write.
func partialSignature(b []byte) bool {
	written := 0
	for written < len(signature) && b[written] == signature[written] {
		written++
	}
	return written > 0 && allErased(b[written:len(signature)])
}

func validKey(key string) bool { return len(key) > 0 && len(key) <= MaxKeySize }

// Use explicit codes instead of persisting Go's reflect.Kind numbers.
func valueKind[T Value]() byte {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	var flag byte
	if typ.Kind() == reflect.Slice {
		flag = kindSlice
		typ = typ.Elem()
	}
	return flag | scalarKinds[typ.Kind()]
}
