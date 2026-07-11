package twitchapi

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxTwitchResponseBytes = 4 << 20

var helixHTTPClient = &http.Client{Timeout: 20 * time.Second}

func readTwitchResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxTwitchResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTwitchResponseBytes {
		return nil, fmt.Errorf("Twitch response exceeds %d bytes", maxTwitchResponseBytes)
	}
	return data, nil
}
