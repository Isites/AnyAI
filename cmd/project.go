package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Isites/anyai/internal/registry"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/Isites/anyai/internal/startup"
)

func runProject(path, overrideAgentID string) error {
	project, err := registry.LoadProject(path)
	if err != nil {
		return err
	}

	entryAgent, err := project.SelectEntry(overrideAgentID)
	if err != nil {
		return err
	}

	result, err := startup.StartGatewayWithConfig(project.Config, version, startup.Options{
		FallbackAgentID: entryAgent.ID,
		LaunchMode:      startup.LaunchModeChat,
	})
	if err != nil {
		return err
	}

	if !result.CanServe() {
		result.Cleanup()
		return fmt.Errorf("start launch did not provision a gateway server")
	}
	go func() {
		if err := result.Serve(); err != nil {
			runtimelogging.Error("project gateway error", "error", err)
			os.Exit(1)
		}
	}()

	defer result.Cleanup()
	if len(result.ActiveChannels()) == 0 {
		return fmt.Errorf("chat launch did not provision any active channels")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return result.Wait(ctx)
}

func runProjectStart(path, overrideAgentID string) error {
	project, err := registry.LoadProject(path)
	if err != nil {
		return err
	}

	entryAgent, err := project.SelectEntry(overrideAgentID)
	if err != nil {
		return err
	}

	result, err := startup.StartGatewayWithConfig(project.Config, version, startup.Options{
		FallbackAgentID: entryAgent.ID,
		LaunchMode:      startup.LaunchModeStart,
	})
	if err != nil {
		return err
	}
	if !result.CanServe() {
		result.Cleanup()
		return fmt.Errorf("start launch did not provision a gateway server")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := result.Serve(); err != nil {
			runtimelogging.Error("project gateway error", "error", err)
			os.Exit(1)
		}
	}()

	<-stop
	result.Cleanup()
	return nil
}
