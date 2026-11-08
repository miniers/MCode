package frontdesk

import (
 "errors"
 "mcode/internal/agent/llm"
 "mcode/internal/apisource"
 "mcode/internal/config"
)
var ErrNoModel=errors.New("没有可用的前台模型")
type Sources interface { Resolve() []apisource.Source; Secrets() config.SecretStore }
func ClientFor(src apisource.Source,secrets config.SecretStore)(llm.Client,string,error) {
 model:=cheapModel(src); if model=="" || src.Kind=="" { return nil,"",ErrNoModel }; key:=src.APIKey(secrets)
 if src.Protocol==apisource.ProtocolAnthropic { return llm.NewAnthropic(llm.AnthropicConfig{BaseURL:src.BaseURL,APIKey:key,Headers:src.Headers}),model,nil }
 return llm.NewOpenAI(llm.OpenAIConfig{BaseURL:src.BaseURL,APIKey:key,Headers:src.Headers}),model,nil
}
