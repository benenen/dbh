//go:build !unix

package dbh

import "os"

func notifyResize() (<-chan os.Signal, func()) {
	return nil, func() {}
}
