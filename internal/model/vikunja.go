package model

type VikunjaHookSettings struct {
	Team      string   `json:"team"`
	HasSecret bool     `json:"hasSecret"`
	Events    []string `json:"events"`
}

type VikunjaHookUpdate struct {
	Secret string   `json:"secret"`
	Events []string `json:"events"`
}
