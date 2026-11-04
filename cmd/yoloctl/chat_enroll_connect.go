package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

type chatControllerCaller func(context.Context, *cobra.Command, string, any, any) error

type chatEnrollConnectResult struct {
	Enrollment      managedchat.Status       `json:"enrollment"`
	Connection      *chatParticipationResult `json:"connection,omitempty"`
	ConnectionError string                   `json:"connection_error,omitempty"`
}

// This is an ordered convenience workflow over existing typed operations.
// Enrollment and connection retain independent receipts and are never replayed.
func runChatEnrollCommand(cmd *cobra.Command, box string, call chatControllerCaller) error {
	connect, _ := cmd.Flags().GetBool("connect")
	client, _ := cmd.Flags().GetString("client")
	if !connect && cmd.Flags().Changed("client") {
		return errors.New("--client requires --connect")
	}
	participation := chatParticipationRequest{Box: box, Client: client}
	if connect {
		if err := validateChatParticipationRequest(participation); err != nil {
			return err
		}
	}
	generation, _ := cmd.Flags().GetUint64("generation")
	recreate, _ := cmd.Flags().GetBool("recreate")
	input := chatRuntimeRequest{Box: box, Generation: generation, Recreate: recreate}
	var status managedchat.Status
	if err := call(cmd.Context(), cmd, "chat.enroll", input, &status); err != nil {
		return err // A lost or failed enrollment reply must never trigger connect.
	}
	if !connect {
		return writeChatStatus(cmd, status)
	}
	if !chatEnrollmentAcknowledged(status, box) {
		return errors.New("enrollment reply lacks the requested active member; inspect chat status before another action; connection was not attempted")
	}
	var connection chatParticipationResult
	connectErr := call(cmd.Context(), cmd, "chat.connect", participation, &connection)
	result := chatEnrollConnectResult{Enrollment: status}
	if connectErr != nil {
		connectErr = fmt.Errorf("box %q enrollment confirmed; client %q connection was not acknowledged; enrollment is retained, inspect chat status and the client's AIRC status before another action: %w", box, client, connectErr)
		result.ConnectionError = connectErr.Error()
	} else {
		result.Connection = &connection
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	var outputErr error
	if jsonOutput {
		outputErr = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	} else {
		outputErr = writeChatStatus(cmd, status)
		if outputErr == nil && connectErr == nil {
			_, outputErr = fmt.Fprintf(cmd.OutOrStdout(), "Connect acknowledged for client %q in box %q; wake listening remains the agent's responsibility.\n", connection.Client, connection.Box)
		}
	}
	return errors.Join(connectErr, outputErr)
}

func chatEnrollmentAcknowledged(status managedchat.Status, box string) bool {
	if status.Room != nil {
		for _, member := range status.Room.Members {
			if member.Name == box && !member.Revoked && member.ID != "" && member.BoxID != "" {
				return true
			}
		}
	}
	return false
}
