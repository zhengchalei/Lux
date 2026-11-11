package review

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lux/internal/domain"
)

// settings.go owns compatible model configuration and Windows-protected provider secrets.
// Public settings expose only credential presence. Blank replacement keys preserve
// the existing secret, while an explicit clear flag removes it. Saved addresses
// accept HTTPS services and local HTTP providers, without embedded URL credentials.
//
// One save lock orders secret preservation, encryption, file replacement, and
// in-memory publication. A failed save leaves the previous usable configuration
// intact. The model client captures a configuration copy before network work,
// allowing settings reads and the interface to remain responsive during analysis.

// providerConfig retains disk-safe preferences; Key contains Windows-protected ciphertext only.
type providerConfig struct {
	BaseURL string `json:"baseUrl"`      // Configured API base address.
	Model   string `json:"model"`        // Configured provider model identifier.
	Key     string `json:"protectedKey"` // Windows-protected base64 credential bytes.
}

// AIService owns provider preferences and constructs bounded evidence-based model requests.
type AIService struct {
	// mu protects config, key, and note together; readers capture immutable copies before HTTP requests, while saveMu serializes complete preference replacements.
	mu     sync.RWMutex   // Protects current provider preferences.
	saveMu sync.Mutex     // Orders concurrent provider setting replacements.
	config providerConfig // Current disk-safe provider preferences.
	key    string         // Decrypted request credential, never serialized.
	note   string         // Public credential recovery explanation.
	path   string         // User-specific provider preference filename.
}

// NewAI restores provider preferences without starting network calls or exposing secrets.
// Unreadable credentials leave address and model available for user correction.
// The returned service starts inactive until an explicit analysis or settings save.
func NewAI(directory string) *AIService {
	s := &AIService{path: filepath.Join(directory, "ai-provider.json")}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	if json.Unmarshal(raw, &s.config) != nil {
		s.note = "AI 设置不可读，请重新保存"
		return s
	}
	if s.config.Key != "" {
		s.key, err = unprotectSecret(s.config.Key)
		if err != nil {
			s.note = "保存的 AI 密钥不可读，请重新填写"
		}
	}
	return s
}

// Settings returns provider metadata and credential presence without the saved key.
func (s *AIService) Settings() domain.AISettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return domain.AISettings{BaseURL: s.config.BaseURL, Model: s.config.Model, HasKey: s.key != "", Note: s.note}
}

// normalizeBase validates a compatible API origin and canonicalizes its base path.
// Plain HTTP is supported only for loopback providers. Redirects are separately
// rejected by the model transport so authentication cannot follow another service.
func normalizeBase(value string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("请输入有效的 AI 接口地址，例如 https://服务地址/v1")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", errors.New("远程 AI 接口需要 HTTPS；本机接口可使用 HTTP")
	}
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	return strings.TrimRight(u.String(), "/"), nil
}

// Save replaces preferences only after a complete encrypted file is safely written.
// Blank keys preserve the current credential unless ClearKey explicitly removes
// it. Secret encryption and file errors return concise recovery messages before
// in-memory configuration changes, keeping an existing working provider available.
// A separate save lock covers credential preservation and replacement together,
// preventing two concurrent edits from pairing one provider with another edit's key.
func (s *AIService) Save(input domain.AISettingsInput) (domain.AISettings, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	base, err := normalizeBase(input.BaseURL)
	if err != nil {
		return s.Settings(), err
	}
	model := strings.TrimSpace(input.Model)
	if model == "" || len(model) > 160 {
		return s.Settings(), errors.New("请填写有效的模型名称")
	}
	if len(input.APIKey) > 8192 || strings.ContainsAny(input.APIKey, "\r\n") {
		return s.Settings(), errors.New("API Key 格式无效")
	}
	s.mu.RLock()
	key := s.key
	s.mu.RUnlock()
	if input.ClearKey {
		key = ""
	}
	if input.APIKey != "" {
		key = input.APIKey
	}
	protected, err := protectSecret(key)
	if err != nil {
		return s.Settings(), err
	}
	next := providerConfig{BaseURL: base, Model: model, Key: protected}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return s.Settings(), errors.New("AI 设置无法编码")
	}
	if os.MkdirAll(filepath.Dir(s.path), 0700) != nil {
		return s.Settings(), errors.New("AI 设置目录无法创建")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ai-*.json")
	if err != nil {
		return s.Settings(), errors.New("AI 设置无法保存")
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(raw)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		return s.Settings(), errors.New("AI 设置无法保存")
	}
	// Publish the closed encrypted document before replacing the in-memory provider so a failed disk replacement cannot discard working settings.
	if os.Rename(tmp.Name(), s.path) != nil {
		return s.Settings(), errors.New("AI 设置无法替换")
	}
	s.mu.Lock()
	s.config, s.key, s.note = next, key, ""
	s.mu.Unlock()
	return s.Settings(), nil
}
