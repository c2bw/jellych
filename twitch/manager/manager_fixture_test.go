package manager

import "github.com/c2bw/jellych/server/api"

func startTestManager(configPath string) (*Manager, error) {
	return StartWithState(configPath, &api.APIState{})
}
