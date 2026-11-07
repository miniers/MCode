//go:build !darwin

package keyring

type unsupportedBackend struct{}

func (b *unsupportedBackend) Get(service, account string) (string, error) {
	return "", ErrUnsupported
}

func (b *unsupportedBackend) Set(service, account, value string) error {
	return ErrUnsupported
}

func (b *unsupportedBackend) Delete(service, account string) error {
	return ErrUnsupported
}

func newPlatformDefaultBackend() Backend {
	return &unsupportedBackend{}
}
