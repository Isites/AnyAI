package gateway

import (
	"fmt"
	"sync"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
)

func (s *Service) runtimeOrErr() (runtimeport.GatewayRuntime, error) {
	if s == nil || s.runtime == nil {
		return nil, fmt.Errorf("runtime not available")
	}
	return s.runtime, nil
}

func replaySeenSet(events []runtimeevents.EventRecord) map[string]struct{} {
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		seen[replayEventKey(event)] = struct{}{}
	}
	return seen
}

func replayEventKey(event runtimeevents.EventRecord) string {
	return fmt.Sprintf("%s/%d/%s", event.RunID, event.Sequence, event.Name)
}

// subscribeReplayStream exposes a single replay surface for snapshot + live
// event consumption. It subscribes first, buffers any live events emitted
// during snapshot loading, then replays the snapshot and flushes only the
// buffered events that were not already present in that snapshot.
func subscribeReplayStream(
	subscribe func() (<-chan runtimeevents.EventRecord, func(), error),
	loadSnapshot func() ([]runtimeevents.EventRecord, error),
) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	raw, rawCancel, err := subscribe()
	if err != nil || rawCancel == nil {
		return nil, nil, nil, err
	}

	out := make(chan runtimeevents.EventRecord, 128)
	done := make(chan struct{})
	release := make(chan map[string]struct{}, 1)

	go func() {
		defer close(out)

		backlog := make([]runtimeevents.EventRecord, 0, 16)
		seen := map[string]struct{}{}
		ready := false

		emit := func(event runtimeevents.EventRecord) bool {
			key := replayEventKey(event)
			if _, ok := seen[key]; ok {
				return true
			}
			seen[key] = struct{}{}
			select {
			case <-done:
				return false
			case out <- event:
				return true
			}
		}

		for {
			if !ready {
				select {
				case <-done:
					return
				case snapshotSeen := <-release:
					seen = snapshotSeen
					for _, event := range backlog {
						if !emit(event) {
							return
						}
					}
					backlog = nil
					ready = true
				case event, ok := <-raw:
					if !ok {
						return
					}
					backlog = append(backlog, event)
				}
				continue
			}

			select {
			case <-done:
				return
			case event, ok := <-raw:
				if !ok {
					return
				}
				if !emit(event) {
					return
				}
			}
		}
	}()

	snapshot, err := loadSnapshot()
	if err != nil {
		close(done)
		rawCancel()
		return nil, nil, nil, err
	}

	release <- replaySeenSet(snapshot)

	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			close(done)
			rawCancel()
		})
	}

	return snapshot, out, cancel, nil
}
