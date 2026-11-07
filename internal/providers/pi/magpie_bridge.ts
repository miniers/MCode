// Magpie 账号桥：pi 扩展。适配器以额外的 `-e` 加载它（见 magpie.go 的 InstallMagpieBridge），
// 只做两件事：
//   1. 读 MCODE_PI_MAGPIE（[{provider,endpoint,baseUrl,apiKey,models}]），把 mcode 设置里的
//      Magpie 端点注册成 pi 的 provider（openai-completions，按需带会话亲和头；
//      deepseek 系模型按 id 声明 thinkingFormat 与 reasoning_content 回传）；
//   2. 每次收到模型回复后，从响应头 x-magpie-provider / x-magpie-model 里取出实际路由到的
//      上游账号，经 ctx.ui.setStatus("mcode-magpie-member", …) 回传给 mcode。
// apiKey 只从环境变量来，绝不进命令行参数。任何异常都只记日志，不影响 pi 正常运行。

const STATUS_KEY = "mcode-magpie-member";
const MAGPIE_PROVIDER_HEADER = "x-magpie-provider";
const MAGPIE_MODEL_HEADER = "x-magpie-model";
// 端点没给数值兜底用的默认窗口与输出上限。
const DEFAULT_CONTEXT_WINDOW = 200000;
const DEFAULT_MAX_TOKENS = 32000;

function errorText(error) {
  return error instanceof Error ? error.message : String(error);
}

// numberOr 取有限的数值，取不到时用兜底值（ProviderModelConfig 的 cost 字段都是必填数字）。
function numberOr(value, fallback) {
  const numeric = typeof value === "number" ? value : Number(value);
  return Number.isFinite(numeric) ? numeric : fallback;
}

// configEndpoints 读端点清单；变量缺失或解析失败时返回空数组（pi 用自己的配置）。
function configEndpoints() {
  try {
    const raw = process.env.MCODE_PI_MAGPIE || "";
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch (error) {
    console.error("mcode magpie: could not read MCODE_PI_MAGPIE: " + errorText(error));
    return [];
  }
}

// isDeepSeekModel 判断模型是否属于 DeepSeek 系：Magpie 网关地址不含 deepseek.com，pi 无法自动识别，
// 需按模型 id 显式声明 DeepSeek 方言。
function isDeepSeekModel(id) {
  return typeof id === "string" && id.toLowerCase().includes("deepseek");
}

// modelConfig 按 ProviderChatModelConfig 补齐缺省字段：cost 缺项补 0，窗口与输出上限给默认值。
function modelConfig(model) {
  const source = model && typeof model === "object" ? model : {};
  const id = typeof source.id === "string" ? source.id : "";
  if (!id) return null;
  const input = Array.isArray(source.input) && source.input.length > 0 ? source.input : ["text"];
  const cost = source.cost && typeof source.cost === "object" ? source.cost : {};
  const config = {
    id,
    name: typeof source.name === "string" && source.name ? source.name : id,
    reasoning: source.reasoning === true,
    input,
    cost: {
      input: numberOr(cost.input, 0),
      output: numberOr(cost.output, 0),
      cacheRead: numberOr(cost.cacheRead, 0),
      cacheWrite: numberOr(cost.cacheWrite, 0),
    },
    contextWindow: numberOr(source.contextWindow, DEFAULT_CONTEXT_WINDOW),
    maxTokens: numberOr(source.maxTokens, DEFAULT_MAX_TOKENS),
    // mcode 的 Magpie 端点按会话亲和路由到具体上游账号，需要 pi 发会话亲和头；
    // DeepSeek 系模型还要补上 thinking 与 reasoning_content 回传的方言（pi 认不出网关域名）。
    compat: {
      sendSessionAffinityHeaders: true,
      ...(isDeepSeekModel(id)
        ? { thinkingFormat: "deepseek", requiresReasoningContentOnAssistantMessages: true }
        : {}),
    },
    // thinkingLevelMap 是可选字段（null 表示该等级不可用），端点没给就不写。
    ...(source.thinkingLevelMap && typeof source.thinkingLevelMap === "object"
      ? { thinkingLevelMap: source.thinkingLevelMap }
      : {}),
  };
  return config;
}

// providerConfig 组装 ProviderConfig；缺 provider 名、baseUrl、apiKey 或模型时返回 null。
function providerConfig(entry) {
  const provider = typeof entry.provider === "string" ? entry.provider : "";
  const baseUrl = typeof entry.baseUrl === "string" ? entry.baseUrl : "";
  const apiKey = typeof entry.apiKey === "string" ? entry.apiKey : "";
  if (!provider || !baseUrl || !apiKey) return null;
  const models = (Array.isArray(entry.models) ? entry.models : []).map(modelConfig).filter((model) => model !== null);
  if (models.length === 0) return null;
  return {
    name: provider,
    baseUrl,
    apiKey,
    api: "openai-completions",
    models,
  };
}

// headerValue 大小写不敏感地从响应头里取值，取不到返回空串。
function headerValue(headers, name) {
  if (!headers || typeof headers !== "object") return "";
  const wanted = String(name).toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (String(key).toLowerCase() !== wanted) continue;
    if (typeof value === "string") return value;
    return value === undefined || value === null ? "" : String(value);
  }
  return "";
}

// registerProviders 逐个注册端点，返回 provider → endpoint 的映射；单个端点出错只跳过它。
function registerProviders(pi) {
  const endpoints = new Map();
  for (const entry of configEndpoints()) {
    if (!entry || typeof entry !== "object") continue;
    try {
      const config = providerConfig(entry);
      if (!config) continue;
      pi.registerProvider(config.name, config);
      endpoints.set(config.name, typeof entry.endpoint === "string" ? entry.endpoint : "");
    } catch (error) {
      console.error("mcode magpie: skipping " + String(entry.provider || "") + ": " + errorText(error));
    }
  }
  return endpoints;
}

export default async function (pi) {
  try {
    const endpoints = registerProviders(pi);
    if (endpoints.size === 0) return;
    pi.on("after_provider_response", (event, ctx) => {
      try {
        const current = ctx && ctx.model ? ctx.model : null;
        const provider = current && typeof current.provider === "string" ? current.provider : "";
        if (!endpoints.has(provider)) return;
        const routedProvider = headerValue(event && event.headers, MAGPIE_PROVIDER_HEADER);
        const routedModel = headerValue(event && event.headers, MAGPIE_MODEL_HEADER);
        // 没有账号头说明这次回复不是经 Magpie 的路由回来的，不回传。
        if (!routedProvider && !routedModel) return;
        // setStatus 在 rpc 模式下是无需应答的通知，适配器据此把账号报给 mcode。
        ctx.ui.setStatus(
          STATUS_KEY,
          JSON.stringify({ endpoint: endpoints.get(provider) || "", provider: routedProvider, model: routedModel }),
        );
      } catch (error) {
        console.error("mcode magpie: could not report member: " + errorText(error));
      }
    });
  } catch (error) {
    // 扩展加载抛错会让整轮起不来，这里只记日志。
    console.error("mcode magpie: " + errorText(error));
  }
}
