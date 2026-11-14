//go:build tinygo && (esp32c3 || esp32s3)

package main

import (
	"time"

	"github.com/Z00mZE/esperetools/kvstorage"
)

func main() {
	time.Sleep(3 * time.Second)
	if err := run(); err != nil {
		println("kvstorage:", err.Error())
	}
	for {
		time.Sleep(time.Hour)
	}
}

func run() error {
	s, err := kvstorage.Open()
	if err != nil {
		return err
	}
	numbers := kvstorage.Typed[uint64](s)
	boots, _, err := numbers.Get("boots")
	if err != nil {
		return err
	}
	if err := numbers.Set("boots", boots+1); err != nil {
		return err
	}
	strings := kvstorage.Typed[string](s)
	name, found, err := strings.Get("name")
	if err != nil {
		return err
	}
	if !found {
		name = "ESP32"
		if err := strings.Set("name", name); err != nil {
			return err
		}
	}
	flags := kvstorage.Typed[bool](s)
	if err := flags.Set("enabled", true); err != nil {
		return err
	}
	lists := kvstorage.Typed[[]uint16](s)
	if _, found, err := lists.Get("ports"); err != nil {
		return err
	} else if !found {
		if err := lists.Set("ports", []uint16{80, 443}); err != nil {
			return err
		}
	}
	ports, _, err := lists.Get("ports")
	if err != nil {
		return err
	}
	enabled, _, err := flags.Get("enabled")
	if err != nil {
		return err
	}
	println("name:", name, "boot:", boots+1, "enabled:", enabled, "ports:", len(ports))
	return nil
}
