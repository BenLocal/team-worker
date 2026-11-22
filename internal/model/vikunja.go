package model

type VikunjaHookSettings struct {
	Team      string            `json:"team"`
	HasSecret bool              `json:"hasSecret"`
	Events    []string          `json:"events"`
	Commands  map[string]string `json:"commands,omitempty"`
}

type VikunjaHookUpdate struct {
	Secret   string            `json:"secret"`
	Events   []string          `json:"events"`
	Commands map[string]string `json:"commands"`
}
