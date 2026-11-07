//go:build !unix

package config

import "os"

func ownerUID(os.FileInfo) (int, bool) { return 0, false }
