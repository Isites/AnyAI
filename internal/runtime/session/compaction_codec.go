package session

import "encoding/json"

func unmarshalMessageData(entry SessionEntry, target *MessageData) error {
	if target == nil {
		return nil
	}
	return json.Unmarshal(entry.Data, target)
}
