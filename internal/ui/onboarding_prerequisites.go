package mcodeui

import "mcode/internal/providers/loginenv"

var onboardingLookPath = loginenv.LookPath

func probeOnboardingPrerequisites() (nodeOK, npmOK bool) {
	_, nodeErr := onboardingLookPath("node")
	_, npmErr := onboardingLookPath("npm")
	return nodeErr == nil, npmErr == nil
}

func onboardingNeedsNode(providerID string) bool {
	switch providerID {
	case "claude", "codex", "pi":
		return true
	default:
		return false
	}
}
