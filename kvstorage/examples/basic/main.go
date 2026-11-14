//go:build tinygo && (esp32c3 || esp32s3)

package main

import (
	"time"

	"github.com/Z00mZE/esperetools/kvstorage"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

func main() {
	time.Sleep(time.Second)
	tries := 10
	// Give the USB serial monitor time to connect after reset.
	for range tries {
		if err := run(); err != nil {
			println("kvstorage error:", err.Error())
			continue
		}

		time.Sleep(time.Second)
	}
}

func run() error {
	for _, tag := range []string{"name", "face"} {
		s, err := kvstorage.Open()
		if err != nil {
			return errors.Wrap(err, "kvstorage.Open")
		}
		strings := kvstorage.Typed[string](s)
		// Read first. A later boot must not overwrite settings with defaults.
		name, found, err := strings.Get(tag)
		if err != nil {
			return errors.Wrap(err, "kvstorage.GetString")
		}

		n := tag + "//" + uuid.NewString()
		println("tag", tag, "founded", found, "old", name, "new", n)
		if err := strings.Set(tag, n); err != nil {
			return errors.Wrap(err, "kvstorage.SetString")
		}
	}
	println("Saved name. Disconnect power, reconnect, and run the monitor.")

	println()
	println()
	return nil
}
