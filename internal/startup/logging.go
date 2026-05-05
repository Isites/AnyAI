package startup

import (
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/logging"
)

func configureRuntimeLogging(cfg *config.Config, launchMode LaunchMode) (*logging.Runtime, error) {
	dataDir := config.DefaultDataDir()
	opts := logging.DefaultOptions(dataDir)
	if cfg != nil {
		dataDir = cfg.RuntimeDataDir()
		opts = logging.DefaultOptions(dataDir)
		if strings.TrimSpace(cfg.Logging.FileLevel) != "" {
			fileLevel, err := logging.ParseLevel(cfg.Logging.FileLevel)
			if err != nil {
				return nil, fmt.Errorf("parse logging.fileLevel: %w", err)
			}
			opts.FileLevel = fileLevel
		}
		if strings.TrimSpace(cfg.Logging.StderrLevel) != "" {
			stderrLevel, err := logging.ParseLevel(cfg.Logging.StderrLevel)
			if err != nil {
				return nil, fmt.Errorf("parse logging.stderrLevel: %w", err)
			}
			opts.StderrLevel = stderrLevel
		}
		if strings.TrimSpace(cfg.Logging.WhatsMeowLevel) != "" {
			whatsMeowLevel, err := logging.ParseLevel(cfg.Logging.WhatsMeowLevel)
			if err != nil {
				return nil, fmt.Errorf("parse logging.whatsMeowLevel: %w", err)
			}
			opts.WhatsMeowLevel = whatsMeowLevel
		}
		opts.Rotation = logging.RotatingFileOptions{
			Filename:   cfg.Logging.Rotation.Filename,
			MaxBytes:   cfg.Logging.Rotation.MaxBytes,
			MaxBackups: cfg.Logging.Rotation.MaxBackups,
		}
		if cfg.Logging.MirrorStderr != nil {
			opts.MirrorStderr = *cfg.Logging.MirrorStderr
		}
	}
	opts.DataDir = dataDir
	if cfg == nil || cfg.Logging.MirrorStderr == nil {
		opts.MirrorStderr = launchMode == LaunchModeStart
	}
	return logging.Install(opts)
}
