package repository

// SettingsRepository abstracts settings persistence
type SettingsRepository interface {
	Load() ([]byte, error)
	Save(data []byte) error
	FindSettingsFile() (string, error)
}
