package vmservice

import (
	"bufio"
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
)

const holderUnitPrefix = "yoloctl-session-"

// InventoryHolders lists every retained holder service, including inactive,
// failed and stopping entries. A failed or unsupported complete inventory must
// not select an initially-absent holder branch. This is a service-manager view,
// not a kernel consumer census or proof of a previous Stop acknowledgement.
func InventoryHolders(ctx context.Context, c *container.Client) ([]string, error) {
	if c == nil {
		return nil, errors.New("holder inventory requires its container client")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	native, err := microvm(c)
	if err != nil {
		return nil, err
	}
	raw, err := readHolderInventory(ctx, c, native)
	if err != nil {
		return nil, err
	}
	return parseHolderInventory(raw, native)
}

func readHolderInventory(ctx context.Context, c *container.Client, native bool) (string, error) {
	return readServiceInventory(ctx, c, native, holderUnitPrefix, holderUnitPrefix+"*")
}

func parseHolderInventory(raw string, native bool) ([]string, error) {
	if len(raw) > serviceInventoryMaxBytes {
		return nil, errors.New("holder service inventory exceeds bound")
	}
	var names []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if native && len(fields) != 1 || !native && len(fields) < 4 {
			return nil, errors.New("incomplete holder service inventory row")
		}
		name := fields[0]
		if !strings.HasPrefix(name, holderUnitPrefix) || !strings.HasSuffix(name, ".service") {
			return nil, errors.New("unexpected holder service inventory member")
		}
		box := strings.TrimSuffix(strings.TrimPrefix(name, holderUnitPrefix), ".service")
		if err := layout.ValidateName("agentbox", box); err != nil {
			return nil, errors.New("invalid holder service inventory identity")
		}
		if seen[name] {
			return nil, errors.New("duplicate holder service inventory identity")
		}
		seen[name] = true
		names = append(names, name)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
