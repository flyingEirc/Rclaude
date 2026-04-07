//go:build !linux

package fusefs

import "context"

// Mount returns ErrUnsupportedPlatform outside Linux.
func Mount(_ context.Context, opts Options) (Mounted, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	_ = defaultedOptions(opts)
	return nil, ErrUnsupportedPlatform
}
