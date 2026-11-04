package agentbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// planNativeCreation selects the original parent identity before native
// reservation or chat/observation preparation can have effects. Non-operator
// native creation keeps its local fresh identity; legacy/privileged creation
// does not call this helper. Publication still needs its separate original
// reservation/parent qualification and explicit readiness ACK.
func planNativeCreation(ctx context.Context, c *Config, image string, current spaceenv.Context, release openshell.QualifiedRelease, policy []byte) (container.RunOptions, nativeContainerPlan, bool, error) {
	refuse := func(err error) (container.RunOptions, nativeContainerPlan, bool, error) {
		return container.RunOptions{}, nativeContainerPlan{}, false, err
	}
	if c == nil || c.Privileged {
		return refuse(errors.New("native creation plan requires an ordinary box"))
	}
	if err := ctx.Err(); err != nil {
		return refuse(err)
	}
	enabled, err := OperatorEnabled(c)
	if err != nil {
		return refuse(err)
	}
	// Every creation gets its own random incarnation, operator-enabled or
	// not, as on the old path (leogr's relaxed decisions): no parent
	// allocation proof is required.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return refuse(err)
	}
	incarnation := hex.EncodeToString(nonce[:])
	options, plan, err := planNativeContainer(c, image, current, release, incarnation, policy)
	if err != nil {
		return refuse(err)
	}
	return options, plan, enabled, nil
}
