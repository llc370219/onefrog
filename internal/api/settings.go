package api

// Legacy response fields remain available alongside the new settings schema.
type settingsView struct {
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	HasAPIKey bool   `json:"has_api_key"`
}
type settingsReq struct {
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
}
