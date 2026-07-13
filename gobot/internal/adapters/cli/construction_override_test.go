package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/stretchr/testify/require"
)

// These tests pin the sp-pdb3 operator surface for the sp-sdyo per-good buy-gating override map:
// the `construction start` LAUNCH flags (--good-override / --overrides) and the boundary
// validation/clamping the CLI applies before an override value is ever persisted. The CLI SETS
// values into the existing override map; it never bypasses a guardrail. In particular the
// price-ceiling multiplier is clamped to the domain hard cap (manufacturing.MaxPriceCeilingMultiplier,
// RULINGS #4) at the boundary, and an unknown strategy/tier is rejected outright.
//
// All cases below are asserted directly against the pure parse/validate helpers rather than through
// cmd.RunE: RunE's happy path falls through to connectDaemon(), which dials a real daemon and blocks
// for seconds with none running (see construction_test.go's file comment). The helpers are the
// actual validation contract; calling them directly keeps the suite instant and deterministic.

func TestParseStrategyFlag_AcceptsEachKnownStrategy(t *testing.T) {
	for _, s := range []string{"prefer-buy", "prefer-fabricate", "smart"} {
		t.Run(s, func(t *testing.T) {
			got, err := parseStrategyFlag(s)
			require.NoError(t, err)
			require.Equal(t, s, got)
		})
	}
}

func TestParseStrategyFlag_UnsetIsValid(t *testing.T) {
	got, err := parseStrategyFlag("")
	require.NoError(t, err)
	require.Equal(t, "", got, "unset strategy must be a valid no-override")
}

func TestParseStrategyFlag_RejectsUnknown(t *testing.T) {
	_, err := parseStrategyFlag("hoard-everything")
	require.Error(t, err)
	require.Contains(t, err.Error(), "hoard-everything")
	require.Contains(t, err.Error(), "strategy")
}

func TestClampPriceCeilingMult_ClampsAboveDomainCap(t *testing.T) {
	got, clamped := clampPriceCeilingMult(9.0)
	require.True(t, clamped, "a value above the domain cap must report that it was clamped")
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, got,
		"the CLI must clamp to the domain hard cap (RULINGS #4), never above it")
}

func TestClampPriceCeilingMult_LeavesInRangeValueUntouched(t *testing.T) {
	got, clamped := clampPriceCeilingMult(2.0)
	require.False(t, clamped)
	require.Equal(t, 2.0, got)
}

func TestClampPriceCeilingMult_ClampsNegativeToZero(t *testing.T) {
	got, clamped := clampPriceCeilingMult(-1.0)
	require.True(t, clamped)
	require.Equal(t, 0.0, got, "a negative multiplier is nonsensical; clamp to 0 (domain treats <=0 as no override)")
}

func TestParseGoodOverrideSpec_ParsesAllKnobs(t *testing.T) {
	good, ov, err := parseGoodOverrideSpec("FAB_MATS:minSupply=LIMITED,strategy=prefer-buy,priceCeilingMult=2.0")
	require.NoError(t, err)
	require.Equal(t, "FAB_MATS", good)
	require.Equal(t, "LIMITED", ov.MinSupply)
	require.Equal(t, "prefer-buy", ov.Strategy)
	require.Equal(t, 2.0, ov.PriceCeilingMult)
}

func TestParseGoodOverrideSpec_ClampsMultAtBoundary(t *testing.T) {
	_, ov, err := parseGoodOverrideSpec("FAB_MATS:priceCeilingMult=99")
	require.NoError(t, err)
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, ov.PriceCeilingMult,
		"a launch-flag mult over the cap is clamped, never persisted above the guardrail")
}

func TestParseGoodOverrideSpec_RejectsUnknownStrategy(t *testing.T) {
	_, _, err := parseGoodOverrideSpec("FAB_MATS:strategy=bogus")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bogus")
}

func TestParseGoodOverrideSpec_RejectsUnknownTier(t *testing.T) {
	_, _, err := parseGoodOverrideSpec("FAB_MATS:minSupply=PLENTIFUL")
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLENTIFUL")
}

func TestParseGoodOverrideSpec_RejectsUnknownKey(t *testing.T) {
	_, _, err := parseGoodOverrideSpec("FAB_MATS:bogusKey=1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bogusKey")
}

func TestParseGoodOverrideSpec_RejectsMissingGood(t *testing.T) {
	_, _, err := parseGoodOverrideSpec("minSupply=LIMITED")
	require.Error(t, err)
}

func TestBuildLaunchGoodOverrides_EmptyInputsYieldNil(t *testing.T) {
	got, err := buildLaunchGoodOverrides(nil, "")
	require.NoError(t, err)
	require.Nil(t, got, "no flags means no overrides — every good keeps the global default")
}

func TestBuildLaunchGoodOverrides_MergesRepeatableSpecs(t *testing.T) {
	got, err := buildLaunchGoodOverrides([]string{
		"FAB_MATS:minSupply=LIMITED,strategy=prefer-buy",
		"ADVANCED_CIRCUITRY:minSupply=MODERATE",
	}, "")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "LIMITED", got["FAB_MATS"].MinSupply)
	require.Equal(t, "prefer-buy", got["FAB_MATS"].Strategy)
	require.Equal(t, "MODERATE", got["ADVANCED_CIRCUITRY"].MinSupply)
}

func TestBuildLaunchGoodOverrides_ParsesJSONBlob(t *testing.T) {
	got, err := buildLaunchGoodOverrides(nil, `{"FAB_MATS":{"minSupply":"LIMITED","strategy":"prefer-buy"}}`)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "LIMITED", got["FAB_MATS"].MinSupply)
	require.Equal(t, "prefer-buy", got["FAB_MATS"].Strategy)
}

func TestBuildLaunchGoodOverrides_JSONBlobClampsAndValidates(t *testing.T) {
	// A fat-finger mult in the JSON blob is clamped at the boundary too — the guardrail is not
	// reachable through --overrides any more than through --good-override.
	got, err := buildLaunchGoodOverrides(nil, `{"FAB_MATS":{"priceCeilingMult":50}}`)
	require.NoError(t, err)
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, got["FAB_MATS"].PriceCeilingMult)

	_, err = buildLaunchGoodOverrides(nil, `{"FAB_MATS":{"strategy":"bogus"}}`)
	require.Error(t, err, "an unknown strategy in the JSON blob is rejected")
}

func TestBuildLaunchGoodOverrides_SpecOverridesJSONForSameGood(t *testing.T) {
	// --good-override is the more explicit, forward CLI form; it wins over --overrides JSON for
	// the same good so an operator can pin one good on the command line while bulk-loading the rest.
	got, err := buildLaunchGoodOverrides(
		[]string{"FAB_MATS:minSupply=SCARCE"},
		`{"FAB_MATS":{"minSupply":"LIMITED"}}`,
	)
	require.NoError(t, err)
	require.Equal(t, "SCARCE", got["FAB_MATS"].MinSupply)
}

// --- live `construction override` verb -----------------------------------------------------------

func TestBuildConstructionOverrideRequest_SetsProvidedKnobsOnly(t *testing.T) {
	req, clamped, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", minSupply: "LIMITED", strategy: "prefer-buy",
	}, 1, nil)
	require.NoError(t, err)
	require.False(t, clamped)
	require.Equal(t, "X1-VB74-I55", req.ConstructionSite)
	require.Equal(t, "FAB_MATS", req.Good)
	require.False(t, req.Clear)
	require.NotNil(t, req.MinSupply)
	require.Equal(t, "LIMITED", *req.MinSupply)
	require.NotNil(t, req.Strategy)
	require.Equal(t, "prefer-buy", *req.Strategy)
	require.Nil(t, req.PriceCeilingMult, "an unset knob must not be sent, so it leaves that dimension unchanged")
}

func TestBuildConstructionOverrideRequest_ClampsMultAtBoundary(t *testing.T) {
	req, clamped, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", priceCeilingMult: 99, multProvided: true,
	}, 1, nil)
	require.NoError(t, err)
	require.True(t, clamped, "a value over the cap reports that it was clamped")
	require.NotNil(t, req.PriceCeilingMult)
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, *req.PriceCeilingMult)
}

func TestBuildConstructionOverrideRequest_RejectsUnknownStrategy(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", strategy: "bogus",
	}, 1, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bogus")
}

func TestBuildConstructionOverrideRequest_RejectsUnknownTier(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", minSupply: "PLENTIFUL",
	}, 1, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLENTIFUL")
}

func TestBuildConstructionOverrideRequest_ClearIsExclusiveOfKnobs(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", clear: true, minSupply: "LIMITED",
	}, 1, nil)
	require.Error(t, err, "--clear cannot be combined with a knob flag")
}

func TestBuildConstructionOverrideRequest_ClearBuildsClearRequest(t *testing.T) {
	req, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", clear: true,
	}, 1, nil)
	require.NoError(t, err)
	require.True(t, req.Clear)
	require.Nil(t, req.MinSupply)
	require.Nil(t, req.Strategy)
	require.Nil(t, req.PriceCeilingMult)
}

func TestBuildConstructionOverrideRequest_RequiresAtLeastOneKnob(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS",
	}, 1, nil)
	require.Error(t, err, "a non-clear override with no knob set has nothing to do")
}

func TestBuildConstructionOverrideRequest_RequiresSiteAndGood(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{good: "FAB_MATS", minSupply: "LIMITED"}, 1, nil)
	require.Error(t, err, "--site is required")

	_, _, err = buildConstructionOverrideRequest(constructionOverrideFlags{site: "X1-VB74-I55", minSupply: "LIMITED"}, 1, nil)
	require.Error(t, err, "--good is required")
}

// fakeConstructionOverrideClient records the request and serves a canned response.
type fakeConstructionOverrideClient struct {
	gotReq  *pb.ConstructionGoodOverrideRequest
	resp    *pb.ConstructionGoodOverrideResponse
	respErr error
}

func (f *fakeConstructionOverrideClient) ConstructionGoodOverride(_ context.Context, req *pb.ConstructionGoodOverrideRequest) (*pb.ConstructionGoodOverrideResponse, error) {
	f.gotReq = req
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

func TestRunConstructionOverride_SetReportsLiveChange(t *testing.T) {
	client := &fakeConstructionOverrideClient{resp: &pb.ConstructionGoodOverrideResponse{
		ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS", Changed: true,
		MinSupply: "LIMITED", Strategy: "prefer-buy",
	}}
	req := &pb.ConstructionGoodOverrideRequest{ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS"}

	msg, err := runConstructionOverride(context.Background(), client, req, false)
	require.NoError(t, err)
	require.Same(t, req, client.gotReq, "the runner must send the request it was given")
	require.Contains(t, msg, "FAB_MATS")
	require.Contains(t, strings.ToLower(msg), "no restart")
}

func TestRunConstructionOverride_ClearReportsRevertToGlobal(t *testing.T) {
	client := &fakeConstructionOverrideClient{resp: &pb.ConstructionGoodOverrideResponse{
		ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS", Cleared: true, Changed: true,
	}}
	req := &pb.ConstructionGoodOverrideRequest{ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS", Clear: true}

	msg, err := runConstructionOverride(context.Background(), client, req, false)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "global default")
}

func TestRunConstructionOverride_NoOpReportsUnchanged(t *testing.T) {
	client := &fakeConstructionOverrideClient{resp: &pb.ConstructionGoodOverrideResponse{
		ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS", Changed: false, MinSupply: "LIMITED",
	}}
	req := &pb.ConstructionGoodOverrideRequest{ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS"}

	msg, err := runConstructionOverride(context.Background(), client, req, false)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "unchanged")
}

func TestRunConstructionOverride_ReportsClamp(t *testing.T) {
	client := &fakeConstructionOverrideClient{resp: &pb.ConstructionGoodOverrideResponse{
		ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS", Changed: true, PriceCeilingMult: manufacturing.MaxPriceCeilingMultiplier,
	}}
	req := &pb.ConstructionGoodOverrideRequest{ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS"}

	msg, err := runConstructionOverride(context.Background(), client, req, true)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "clamp")
}

func TestRunConstructionOverride_ErrorPropagates(t *testing.T) {
	client := &fakeConstructionOverrideClient{respErr: errors.New("no active construction pipeline for X1-VB74-I55")}
	req := &pb.ConstructionGoodOverrideRequest{ConstructionSite: "X1-VB74-I55", Good: "FAB_MATS"}

	_, err := runConstructionOverride(context.Background(), client, req, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "FAB_MATS")
}
