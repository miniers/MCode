package backup

import (
	"os"

	"mcode/internal/config"
)

var SecretStoreFor = func(service string) config.SecretStore {
	return config.NewSecretStore(service)
}

type CreateOptions struct {
	DataDir        string
	Destination    string
	Password       []byte
	IncludeHistory bool
}

type StageOptions struct {
	DataDir  string
	Source   string
	Password []byte
}

type Info struct{ Version uint16 }

type SecretFailure struct{ Service, Account, Reason string }

type Result struct {
	Applied           bool
	RollbackDir       string
	SecretFailures    []SecretFailure
	InvalidProjectIDs []string
}

// Inspect reads only the outer header; it does not authenticate the archive.
func Inspect(path string) (Info, error) {
	file, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer file.Close()
	header, err := ReadHeader(file)
	if err != nil {
		return Info{}, err
	}
	return Info{Version: header.Version}, nil
}
