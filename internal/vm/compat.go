package vm

import "github.com/leogr/yoloctl/internal/lima"

// Lima-specific compatibility entry points are confined to legacy migration,
// diagnostics and stderr presentation. New VM operations use Driver methods.
var (
	SetupHome                = lima.SetupHome
	Command                  = lima.Command
	CommandForSpace          = lima.CommandForSpace
	InstanceName             = lima.InstanceName
	TemplateWithAccountDisk  = lima.TemplateWithAccountDisk
	TemplateWithBootSettings = lima.TemplateWithBootSettings
	MarshalMounts            = lima.MarshalMounts
	RunWithFilteredStderr    = lima.RunWithFilteredStderr
	AttachFilteredStderr     = lima.AttachFilteredStderr
	WriteFilteredStderr      = lima.WriteFilteredStderr
	LogDiagnostics           = lima.LogDiagnostics
	FilterStderrText         = lima.FilterStderrText
	DebugEnabled             = lima.DebugEnabled
)
