//go:build windows

package testutil

func lockFile(string) (func(), error) {
	return func() {}, nil
}
