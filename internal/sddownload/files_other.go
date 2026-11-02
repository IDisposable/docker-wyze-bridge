//go:build !linux

package sddownload

import "os"

// The optional go2rtc helper is currently Linux-only. Keep native bridge builds
// usable on other platforms with the feature unavailable.
func openPrivate(root *os.Root, name string) (*os.File, error) {
	return nil, os.ErrInvalid
}
