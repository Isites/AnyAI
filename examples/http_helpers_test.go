package examples

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func fetchJSON(url string, target any) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
