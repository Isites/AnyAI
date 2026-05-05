package runtimeevents

import "strings"

// EventCategory is the top-level domain classification for one runtime event.
type EventCategory string

const (
	EventCategoryUnknown     EventCategory = "unknown"
	EventCategoryRun         EventCategory = "run"
	EventCategoryTask        EventCategory = "task"
	EventCategoryTool        EventCategory = "tool"
	EventCategoryToolCall    EventCategory = "tool_call"
	EventCategoryAgentCall   EventCategory = "agent_call"
	EventCategoryMemory      EventCategory = "memory"
	EventCategoryAttachment  EventCategory = "attachment"
	EventCategoryInput       EventCategory = "input"
	EventCategorySession     EventCategory = "session"
	EventCategoryConfig      EventCategory = "config"
	EventCategoryMaintenance EventCategory = "maintenance"
	EventCategoryFallback    EventCategory = "fallback"
	EventCategoryLLM         EventCategory = "llm"
)

// TypedEvent is one recorder event plus its normalized event-module category.
type TypedEvent struct {
	Record   EventRecord
	Category EventCategory
	Action   string
}

// Listener consumes typed runtime events dispatched by the recorder.
type Listener interface {
	HandleEvent(TypedEvent)
}

// ListenerFunc adapts a function into a Listener.
type ListenerFunc func(TypedEvent)

func (f ListenerFunc) HandleEvent(event TypedEvent) {
	if f != nil {
		f(event)
	}
}

// ClassifyEvent normalizes the event name into a stable category/action pair.
func ClassifyEvent(record EventRecord) TypedEvent {
	name := strings.TrimSpace(record.Name)
	category := EventCategoryUnknown
	action := name

	switch {
	case strings.HasPrefix(name, "agent.call."):
		category = EventCategoryAgentCall
		action = strings.TrimPrefix(name, "agent.call.")
	case strings.HasPrefix(name, "tool.call."):
		category = EventCategoryToolCall
		action = strings.TrimPrefix(name, "tool.call.")
	case strings.HasPrefix(name, "tool."):
		category = EventCategoryTool
		action = strings.TrimPrefix(name, "tool.")
	case strings.HasPrefix(name, "run."):
		category = EventCategoryRun
		action = strings.TrimPrefix(name, "run.")
	case strings.HasPrefix(name, "task."):
		category = EventCategoryTask
		action = strings.TrimPrefix(name, "task.")
	case strings.HasPrefix(name, "memory."):
		category = EventCategoryMemory
		action = strings.TrimPrefix(name, "memory.")
	case strings.HasPrefix(name, "attachment."):
		category = EventCategoryAttachment
		action = strings.TrimPrefix(name, "attachment.")
	case strings.HasPrefix(name, "input."):
		category = EventCategoryInput
		action = strings.TrimPrefix(name, "input.")
	case strings.HasPrefix(name, "session."):
		category = EventCategorySession
		action = strings.TrimPrefix(name, "session.")
	case strings.HasPrefix(name, "config."):
		category = EventCategoryConfig
		action = strings.TrimPrefix(name, "config.")
	case strings.HasPrefix(name, "maintenance."):
		category = EventCategoryMaintenance
		action = strings.TrimPrefix(name, "maintenance.")
	case strings.HasPrefix(name, "fallback."):
		category = EventCategoryFallback
		action = strings.TrimPrefix(name, "fallback.")
	case strings.HasPrefix(name, "llm."):
		category = EventCategoryLLM
		action = strings.TrimPrefix(name, "llm.")
	}

	return TypedEvent{
		Record:   record,
		Category: category,
		Action:   strings.TrimSpace(action),
	}
}
