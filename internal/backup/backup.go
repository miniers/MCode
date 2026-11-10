package backup

import "mcode/internal/config"

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

type SecretFailure struct{ Service, Account, Reason string }

type Result struct {
	Applied           bool
	RollbackDir       string
	SecretFailures    []SecretFailure
	InvalidProjectIDs []string
	Error             string `json:"error,omitempty"`
}
