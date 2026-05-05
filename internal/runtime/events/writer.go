package runtimeevents

func AppendEventWithReplayPolicy(recorder *Recorder, event EventRecord) {
	if recorder == nil {
		return
	}
	switch event.Name {
	case EventTaskRunning:
		recorder.PublishTransientEvent(event)
	default:
		recorder.AppendEvent(event)
	}
}
