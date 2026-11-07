package mcodeui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"mcode/internal/config"
)

const (
	onboardingPending   = "pending"
	onboardingCompleted = "completed"
)

var onboardingAutoOpen = struct {
	sync.Mutex
	states map[*chatState]bool
}{states: make(map[*chatState]bool)}

// PrepareOnboarding 识别全新安装，并记录尚未完成的引导。
func PrepareOnboarding(dataDir string) (bool, error) {
	SetUIStateDataDir(dataDir)
	lastSelectionWriteMu.Lock()
	defer lastSelectionWriteMu.Unlock()

	file, exists, err := loadUIStateFileStrict()
	if err != nil {
		return false, err
	}
	if exists {
		return file.Onboarding == onboardingPending, nil
	}
	for _, path := range []string{config.ConfigPath(dataDir), filepath.Join(dataDir, "mcode.db")} {
		_, err := os.Stat(path)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	data, err := json.Marshal(map[string]string{"onboarding": onboardingPending})
	if err != nil {
		return false, err
	}
	_, err = writeUIStateBytes(uiStatePath(), data)
	return err == nil, err
}

// CompleteOnboarding 记录引导已完成，并保留其他界面状态。
func CompleteOnboarding() error {
	lastSelectionWriteMu.Lock()
	defer lastSelectionWriteMu.Unlock()

	_, exists, err := loadUIStateFileStrict()
	if err != nil {
		return err
	}
	fields := make(map[string]json.RawMessage)
	if exists {
		data, err := os.ReadFile(uiStatePath())
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		if fields == nil {
			fields = make(map[string]json.RawMessage)
		}
	}
	value, err := json.Marshal(onboardingCompleted)
	if err != nil {
		return err
	}
	fields["onboarding"] = value
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeUIStateBytes(uiStatePath(), data)
	return err
}

// WithOnboarding 为指定窗口设置一次性的引导自动打开状态。
func WithOnboarding(autoOpen bool) Option {
	return func(s *chatState) {
		if s == nil {
			return
		}
		onboardingAutoOpen.Lock()
		if autoOpen {
			onboardingAutoOpen.states[s] = true
		} else {
			delete(onboardingAutoOpen.states, s)
		}
		onboardingAutoOpen.Unlock()
	}
}

func takeOnboardingAutoOpen(s *chatState) bool {
	onboardingAutoOpen.Lock()
	defer onboardingAutoOpen.Unlock()
	autoOpen := onboardingAutoOpen.states[s]
	delete(onboardingAutoOpen.states, s)
	return autoOpen
}
