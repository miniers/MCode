package frontdesk

import (
	"errors"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
)

// ErrNoModel 表示没有可用的前台模型。
var ErrNoModel = errors.New("没有可用的前台模型")

// Sources 提供模型来源和凭据。
type Sources interface {
	Resolve() []apisource.Source
	Secrets() config.SecretStore
}

// ClientFor 根据来源创建模型客户端。
func ClientFor(src apisource.Source, secrets config.SecretStore) (llm.Client, string, error) {
	model := cheapModel(src)
	if model == "" || src.Kind == "" {
		return nil, "", ErrNoModel
	}
	key := src.APIKey(secrets)
	return apisource.NewClient(src, key, model, apisource.ClientOptions{}), model, nil
}
