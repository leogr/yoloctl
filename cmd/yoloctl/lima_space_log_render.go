package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Lima's ha.stdout.log contains hostagent/events.Event records, not logrus
// messages. Keep this presentation adapter separate from other VM log formats.
// Unknown fields/types retain the whole original line rather than lose detail.
type limaSpaceEvent struct {
	Time   time.Time        `json:"time"`
	Status *limaSpaceStatus `json:"status"`
}

type limaSpaceStatus struct {
	Running           bool               `json:"running"`
	Degraded          bool               `json:"degraded"`
	Exiting           bool               `json:"exiting"`
	Errors            []string           `json:"errors"`
	SSHLocalPort      int                `json:"sshLocalPort"`
	PortForward       *limaPortEvent     `json:"portForward"`
	Vsock             *limaVsockEvent    `json:"vsock"`
	CloudInitProgress *limaCloudProgress `json:"cloudInitProgress"`
}

type limaPortEvent struct {
	Type      string `json:"type"`
	Protocol  string `json:"protocol"`
	GuestAddr string `json:"guestAddr"`
	HostAddr  string `json:"hostAddr"`
	Error     string `json:"error"`
}

type limaVsockEvent struct {
	Type      string `json:"type"`
	HostAddr  string `json:"hostAddr"`
	VsockPort int    `json:"vsockPort"`
	Reason    string `json:"reason"`
}

type limaCloudProgress struct {
	LogLine   string `json:"logLine"`
	Completed bool   `json:"completed"`
	Active    bool   `json:"active"`
}

func limaSpaceLogRecord(line string) slog.Record {
	fallback := spaceLogRecord(line)
	if !utf8.ValidString(line) {
		return fallback
	}
	var event limaSpaceEvent
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&event) != nil || event.Time.IsZero() || event.Status == nil || decoder.Decode(new(any)) != io.EOF {
		return fallback
	}
	status := event.Status
	level := slog.LevelInfo
	var parts []string
	if port := status.PortForward; port != nil {
		var message string
		switch port.Type {
		case "forwarding":
			message = "Forwarding"
		case "not-forwarding":
			message = "Not forwarding"
		case "stopping":
			message = "Stopping"
		case "failed":
			message = "Port forward failed"
			level = slog.LevelWarn
		default:
			return fallback
		}
		if port.Protocol != "" {
			message += " " + port.Protocol
		}
		if port.Type == "stopping" {
			message += " forward"
		}
		if port.HostAddr != "" {
			message += " " + port.HostAddr
		}
		if port.GuestAddr != "" {
			if port.HostAddr != "" {
				message += " →"
			}
			message += " " + port.GuestAddr
		}
		if port.Error != "" {
			message += ": " + port.Error
			level = slog.LevelWarn
		}
		parts = append(parts, message)
	}
	if vsock := status.Vsock; vsock != nil {
		var message string
		switch vsock.Type {
		case "started":
			message = "Vsock forwarding started"
		case "skipped":
			message = "Vsock forwarding skipped"
		case "failed":
			message = "Vsock forwarding failed"
			level = slog.LevelWarn
		default:
			return fallback
		}
		if vsock.HostAddr != "" {
			message += " " + vsock.HostAddr
		}
		if vsock.VsockPort != 0 {
			message += fmt.Sprintf(" → port %d", vsock.VsockPort)
		}
		if vsock.Reason != "" {
			message += ": " + vsock.Reason
		}
		parts = append(parts, message)
	}
	if progress := status.CloudInitProgress; progress != nil {
		var message string
		switch {
		case progress.Completed:
			message = "Cloud-init completed"
		case progress.LogLine != "":
			message = "Cloud-init"
		case progress.Active:
			message = "Cloud-init monitoring active"
		default:
			return fallback
		}
		if progress.LogLine != "" {
			message += ": " + progress.LogLine
		}
		parts = append(parts, message)
	}
	// State fields are repeated snapshots on detailed events. Show the state
	// instead on standalone state records; always retain degradation and errors.
	if status.Degraded {
		if len(parts) == 0 && status.Running {
			parts = append(parts, "VM running (degraded)")
		} else {
			parts = append(parts, "VM degraded")
		}
		level = slog.LevelWarn
	} else if status.Exiting {
		parts = append(parts, "VM exiting")
	} else if len(parts) == 0 && status.Running {
		parts = append(parts, "VM running")
	}
	if status.SSHLocalPort != 0 && status.PortForward == nil && status.Vsock == nil && status.CloudInitProgress == nil {
		parts = append(parts, fmt.Sprintf("SSH local port %d", status.SSHLocalPort))
	}
	if len(status.Errors) != 0 {
		parts = append(parts, "Errors: "+strings.Join(status.Errors, "; "))
		level = slog.LevelError
	}
	if len(parts) == 0 {
		return fallback
	}
	record := slog.NewRecord(event.Time, level, strings.Join(parts, "; "), 0)
	record.AddAttrs(prettylog.Actor(logging.VM), prettylog.Executor("lima"))
	return record
}
