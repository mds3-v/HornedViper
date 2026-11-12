package config

// Preset is a provider template shown on the "new provider" page.
type Preset struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Format      string `json:"format"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	Category    string `json:"category"`
}

// Presets returns the built-in provider templates. Endpoints/models are
// editable after creation; unknown endpoints are intentionally left blank.
func Presets() []Preset {
	return []Preset{
		{
			Key: "openai", Name: "OpenAI Official", Category: "官方",
			Description: "Responses API · 聊天补全",
			Format:      "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-5.5",
		},
		{
			Key: "deepseek", Name: "DeepSeek", Category: "官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash",
		},
		{
			Key: "anthropic", Name: "Anthropic Claude", Category: "官方",
			Description: "Anthropic Messages 接口",
			Format:      "anthropic", BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-4-20250514",
		},
		{
			Key: "zhipu", Name: "Zhipu GLM", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-5.1",
		},
		{
			Key: "kimi", Name: "Kimi", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://api.moonshot.cn/v1", Model: "kimi-k2.6",
		},
		{
			Key: "qwen", Name: "Bailian (Qwen)", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen3-coder-plus",
		},
		{
			Key: "stepfun", Name: "StepFun", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://api.stepfun.com/v1", Model: "step-3.5-flash-2603",
		},
		{
			Key: "minimax", Name: "MiniMax", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://api.minimax.chat/v1", Model: "MiniMax-M2.7",
		},
		{
			Key: "ark", Name: "火山引擎 Ark", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Model: "ark-code-latest",
		},
		{
			Key: "qianfan", Name: "百度千帆 Coding Plan", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://qianfan.baidubce.com/v2", Model: "qianfan-code-latest",
		},
		{
			Key: "mimo", Name: "小米 MiMo", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "", Model: "mimo-v2.5-pro",
		},
		{
			Key: "modelscope", Name: "ModelScope", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "https://api-inference.modelscope.cn/v1", Model: "ZhipuAI/GLM-5.1",
		},
		{
			Key: "longcat", Name: "Longcat", Category: "中国官方",
			Description: "纯 API · 聊天补全",
			Format:      "openai", BaseURL: "", Model: "LongCat-Flash-Chat",
		},
	}
}

func PresetByKey(key string) (Preset, bool) {
	for _, p := range Presets() {
		if p.Key == key {
			return p, true
		}
	}
	return Preset{}, false
}

// PresetCategories returns categories in display order.
func PresetCategories() []string {
	return []string{"官方", "中国官方"}
}
