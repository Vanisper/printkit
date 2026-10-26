//go:build !windows

package render

func enterWorkerProcess() (func() error, error) {
	return func() error { return nil }, nil
}
