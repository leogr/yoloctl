package controller

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/imagejob"
)

func localJobRecipe(t *testing.T) ImageRecipe {
	t.Helper()
	r, err := imagejob.NewLocalRetain("custom/cache:one", "linux/amd64", imagejob.LocalTarget{
		Digest: "sha256:" + strings.Repeat("a", 64), Size: 123, MediaType: "application/vnd.oci.image.manifest.v1+json",
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLocalImageJobKeepsOriginalReceiptAndSourceDependency(t *testing.T) {
	m, b := newJobs(t, 2)
	r := localJobRecipe(t)
	result := acquire(m, t.Context(), r)
	phase := take(t, b.started)
	receipt, err := m.server.Receipts.Read(phase.JobID)
	if err != nil || receipt.EffectPhase != EffectStarted || receipt.Image == nil || receipt.Image.Stage != "build" {
		t.Fatal("retention reached backend before original image permission", receipt, err)
	}
	source := Resource{Kind: ResourceImages, Name: r.BaseReference}
	if !containsResource(receipt.Scope.Reads, source) || containsResource(receipt.Scope.Writes, source) || !containsResource(receipt.Scope.Writes, Resource{Kind: ResourceImages, Name: phase.PreparedRef}) {
		t.Fatal("local retain lost source dependency or reserved public retag", receipt.Scope)
	}
	b.release <- struct{}{}
	got := take(t, result)
	if got.err != nil || got.image == nil || got.image.Digest != r.LocalTarget.Digest {
		t.Fatal("retained result lost its original job target", got.err)
	}
	got.image.Release()
	receipt, err = m.server.Receipts.Read(phase.JobID)
	if err != nil || receipt.Completion == nil || receipt.Completion.Status != 0 || receipt.Image.ReleasedAt.IsZero() {
		t.Fatal("normal consumer release bypassed durable image completion", receipt, err)
	}
}

func TestLocalImageJobCannotBypassHeldSourceThroughCanonicalAlias(t *testing.T) {
	m, b := newJobs(t, 2)
	r := localJobRecipe(t)
	seedReceipt(t, m.server, "held-local-image-source-123", "uncertain", writes(Resource{Kind: ResourceImages, Name: "custom/cache:one"}), func(receipt *Receipt) { receipt.ProofVersion = 2 })
	if image, err := m.Acquire(t.Context(), r); err == nil || image != nil {
		t.Fatal("retention bypassed held current source", image, err)
	}
	select {
	case <-b.started:
		t.Fatal("held source reached a lease/alias effect")
	default:
	}
}
