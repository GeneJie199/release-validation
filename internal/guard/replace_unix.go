//go:build !windows

package guard

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }
