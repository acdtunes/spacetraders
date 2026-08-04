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

// These tests pin the operator surface for the sp-sdyo per-good buy-gating override map:
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
		site: "X1-VB74-I55", good: "FAB_MATS", minSupply: "LIMITED",
	}, &PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err)
	require.False(t, clamped)
	require.Equal(t, "X1-VB74-I55", req.ConstructionSite)
	require.Equal(t, "FAB_MATS", req.Good)
	require.False(t, req.Clear)
	require.NotNil(t, req.MinSupply)
	require.Equal(t, "LIMITED", *req.MinSupply)
	require.Nil(t, req.PriceCeilingMult, "an unset knob must not be sent, so it leaves that dimension unchanged")
	require.Equal(t, int32(1), req.PlayerId, "the override must be addressed to the resolved player")
	require.Nil(t, req.AgentSymbol, "a numeric identifier sends no agent symbol")
}

// TestBuildConstructionOverrideRequest_AddressesTheResolvedPlayer pins the request's
// player addressing for both identifier forms. The override targets ONE player's running
// construction pipeline, so a wrong or absent identifier tunes the wrong pipeline (or none):
// the numeric form must send the id with no agent symbol, and the agent form must send the
// symbol with the id left at its zero value.
func TestBuildConstructionOverrideRequest_AddressesTheResolvedPlayer(t *testing.T) {
	flags := constructionOverrideFlags{site: "X1-VB74-I55", good: "FAB_MATS", minSupply: "LIMITED"}

	byID, _, err := buildConstructionOverrideRequest(flags, &PlayerIdentifier{PlayerID: 7})
	require.NoError(t, err)
	require.Equal(t, int32(7), byID.PlayerId)
	require.Nil(t, byID.AgentSymbol)

	byAgent, _, err := buildConstructionOverrideRequest(flags, &PlayerIdentifier{AgentSymbol: "ENDURANCE"})
	require.NoError(t, err)
	require.Zero(t, byAgent.PlayerId, "an agent-symbol identifier carries no numeric id")
	require.NotNil(t, byAgent.AgentSymbol)
	require.Equal(t, "ENDURANCE", *byAgent.AgentSymbol)
}

func TestBuildConstructionOverrideRequest_ClampsMultAtBoundary(t *testing.T) {
	req, clamped, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", priceCeilingMult: 99, multProvided: true,
	}, &PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err)
	require.True(t, clamped, "a value over the cap reports that it was clamped")
	require.NotNil(t, req.PriceCeilingMult)
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, *req.PriceCeilingMult)
}

func TestBuildConstructionOverrideRequest_RejectsUnknownTier(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", minSupply: "PLENTIFUL",
	}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLENTIFUL")
}

func TestBuildConstructionOverrideRequest_ClearIsExclusiveOfKnobs(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", clear: true, minSupply: "LIMITED",
	}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--clear cannot be combined with a knob flag")
}

func TestBuildConstructionOverrideRequest_ClearBuildsClearRequest(t *testing.T) {
	req, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS", clear: true,
	}, &PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err)
	require.True(t, req.Clear)
	require.Nil(t, req.MinSupply)
	require.Nil(t, req.PriceCeilingMult)
}

func TestBuildConstructionOverrideRequest_RequiresAtLeastOneKnob(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{
		site: "X1-VB74-I55", good: "FAB_MATS",
	}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "a non-clear override with no knob set has nothing to do")
}

func TestBuildConstructionOverrideRequest_RequiresSiteAndGood(t *testing.T) {
	_, _, err := buildConstructionOverrideRequest(constructionOverrideFlags{good: "FAB_MATS", minSupply: "LIMITED"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--site is required")

	_, _, err = buildConstructionOverrideRequest(constructionOverrideFlags{site: "X1-VB74-I55", minSupply: "LIMITED"}, &PlayerIdentifier{PlayerID: 1})
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
		MinSupply: "LIMITED",
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

// --- pipeline-wide gate DELIVERY floors: the second RPC behind the same verb -----------------------
//
// These pin the operator surface for --buy-floor/--resume-floor. They are PIPELINE-WIDE
// (no --good), so they get their own request shape and their own RPC; the verb dispatches
// on which flags were set. Everything here is asserted against the pure builder/runner for
// the same reason the per-good cases above are: RunE's happy path dials a real daemon.

// The pipeline-wide floors are a DIFFERENT decision from the per-good override: they have
// no good. Passing --good with them is a mistake worth rejecting loudly rather than
// silently ignoring, because an operator who typed it believes it did something.
func TestBuildConstructionDeliveryFloorsRequest_RejectsGoodAndRequiresSite(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", good: "FAB_MATS", buyFloor: "MODERATE"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--good must be rejected: the delivery floors are pipeline-wide")

	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{buyFloor: "MODERATE"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--site is required")

	// --clear removes a PER-GOOD override; there is no such thing as clearing a
	// pipeline-wide floor (unset resolves to the armed default, it is not an off switch),
	// so combining them is ambiguous rather than merely redundant.
	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", clear: true, buyFloor: "MODERATE"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--clear cannot be combined with the pipeline-wide floors")
}

// Tier values are validated at the CLI boundary. ParseSupplyLevel is deliberately lenient
// (unknown -> MODERATE) for scanned market data; operator input is not, or a typo would
// silently become a floor the operator never chose.
//
// The ADVERTISED vocabulary must be the ACCEPTED vocabulary, per flag. The two floors do not
// take the same set — ABUNDANT cannot be a buy floor and SCARCE cannot be a resume floor
// (see the two ladder-end tests below) — so a refusal that listed all five would answer a
// rejected value with a menu whose obvious next pick is rejected too.
func TestBuildConstructionDeliveryFloorsRequest_RejectsAnInvalidTier(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "PLENTIFUL"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLENTIFUL")
	require.Contains(t, err.Error(), "SCARCE", "the refusal must list the accepted vocabulary")
	require.NotContains(t, err.Error(), "ABUNDANT",
		"--buy-floor must not advertise ABUNDANT: it is refused, so offering it sends the operator to a second rejection")

	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", resumeFloor: "LOTS"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "LOTS")
	require.Contains(t, err.Error(), "ABUNDANT", "the refusal must list the accepted vocabulary")
	require.NotContains(t, err.Error(), "SCARCE",
		"--resume-floor must not advertise SCARCE: it is refused, so offering it sends the operator to a second rejection")
}

// The two per-flag vocabularies must stay the canonical five minus exactly one end each, or
// a level could be silently dropped from an operator's reach (or a rejected one silently
// re-offered) without any test noticing. Pins them against the --min-supply whitelist, which
// is the full ladder.
func TestDeliveryFloorVocabularies_AreTheFullLadderMinusOneEndEach(t *testing.T) {
	require.ElementsMatch(t,
		[]manufacturing.SupplyLevel{
			manufacturing.SupplyLevelHigh, manufacturing.SupplyLevelModerate,
			manufacturing.SupplyLevelLimited, manufacturing.SupplyLevelScarce,
		}, validBuyFloors, "--buy-floor is the full ladder minus ABUNDANT (the top)")

	require.ElementsMatch(t,
		[]manufacturing.SupplyLevel{
			manufacturing.SupplyLevelAbundant, manufacturing.SupplyLevelHigh,
			manufacturing.SupplyLevelModerate, manufacturing.SupplyLevelLimited,
		}, validResumeFloors, "--resume-floor is the full ladder minus SCARCE (the bottom)")

	// Neither list may drift away from the canonical ladder it is carved out of.
	require.Subset(t, validMinSupplyLevels, validBuyFloors)
	require.Subset(t, validMinSupplyLevels, validResumeFloors)
	require.Len(t, validMinSupplyLevels, 5)
}

// A resume floor at or below the buy floor collapses the hysteresis to the single
// threshold that chatters. Reject it at the boundary, naming both values, rather than
// letting the domain silently raise it -- an operator who asked for a gap of zero has a
// mental model worth correcting.
func TestBuildConstructionDeliveryFloorsRequest_RejectsANonPositiveHysteresisGap(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "HIGH", resumeFloor: "MODERATE"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "HIGH")
	require.Contains(t, err.Error(), "MODERATE")

	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "HIGH", resumeFloor: "HIGH"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "an equal resume floor is a zero gap")
}

// ABUNDANT is the top of the supply ladder, so NO resume floor can sit strictly above it:
// gate.nextLevelAbove(ABUNDANT) returns ABUNDANT and the hysteresis gap collapses to zero.
// A one-sided --buy-floor ABUNDANT slips past the pairwise gap check (there is no second
// value to compare against), so it is refused on its own -- otherwise the only thing
// standing between the operator and a chattering fleet is a daemon round-trip whose error
// names a --resume-floor they never typed.
func TestBuildConstructionDeliveryFloorsRequest_RejectsATopOfLadderBuyFloor(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "ABUNDANT"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "no resume floor can sit above ABUNDANT, so it is not a settable buy floor")
	require.Contains(t, err.Error(), "ABUNDANT")

	// ABUNDANT remains a legal RESUME floor -- it is the strictest recovery bar, and the
	// gap above a lower buy floor is real.
	req, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "HIGH", resumeFloor: "ABUNDANT"},
		&PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err, "ABUNDANT is a valid resume floor above a lower buy floor")
	require.Equal(t, "ABUNDANT", *req.ResumeFloor)
}

// The MIRROR of the ABUNDANT case, and the worse of the two because it had no guard at all.
// SCARCE is the bottom of the ladder (Order 1), so effectiveResume.Order() <= effectiveBuy.Order()
// holds for EVERY buy floor -- a one-sided --resume-floor SCARCE slips past the pairwise check
// (which needs both flags) and dies at the daemon with an error naming a --buy-floor the
// operator never typed. That is word for word the failure mode the ABUNDANT guard exists to
// prevent, so it is refused at the same boundary with an equally explanatory message.
func TestBuildConstructionDeliveryFloorsRequest_RejectsABottomOfLadderResumeFloor(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", resumeFloor: "SCARCE"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "no buy floor can sit below SCARCE, so it is not a settable resume floor")
	require.Contains(t, err.Error(), "SCARCE")
	require.Contains(t, strings.ToLower(err.Error()), "bottom",
		"the refusal must explain WHY, like its ABUNDANT mirror, not just reject")

	// SCARCE remains a legal BUY floor -- it is the most permissive buying bar, and a resume
	// floor above it is expressible.
	req, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "SCARCE", resumeFloor: "LIMITED"},
		&PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err, "SCARCE is a valid buy floor below a higher resume floor")
	require.Equal(t, "SCARCE", *req.BuyFloor)
}

// Setting ONE floor is legal: the other keeps whatever is already persisted. The request
// carries only the provided knob, so an unset one leaves that dimension unchanged --
// matching how --min-supply/--price-ceiling-mult already behave on this verb.
func TestBuildConstructionDeliveryFloorsRequest_SendsOnlyTheProvidedFloors(t *testing.T) {
	req, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "LIMITED"}, &PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err)
	require.NotNil(t, req.BuyFloor)
	require.Equal(t, "LIMITED", *req.BuyFloor)
	require.Nil(t, req.ResumeFloor, "an unset resume floor must leave that dimension unchanged")
	require.Equal(t, "X1-VB74-I55", req.ConstructionSite)
	require.NotNil(t, req.PlayerId)
	require.Equal(t, int32(1), *req.PlayerId, "the resolved player must reach the daemon")
}

// fakeConstructionFloorsClient records the request and serves a canned response.
type fakeConstructionFloorsClient struct {
	gotReq  *pb.ConstructionDeliveryFloorsRequest
	resp    *pb.ConstructionDeliveryFloorsResponse
	respErr error
}

func (f *fakeConstructionFloorsClient) ConstructionDeliveryFloors(_ context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error) {
	f.gotReq = req
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

// The confirmation must state the RESOLVED floors in force and that no restart is needed
// -- the operator's only evidence the knob is live.
func TestRunConstructionDeliveryFloors_ReportsTheResolvedFloorsAndLiveness(t *testing.T) {
	client := &fakeConstructionFloorsClient{resp: &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite: "X1-VB74-I55", BuyFloor: "LIMITED", ResumeFloor: "MODERATE", Changed: true,
	}}
	req := &pb.ConstructionDeliveryFloorsRequest{ConstructionSite: "X1-VB74-I55"}

	msg, err := runConstructionDeliveryFloors(context.Background(), client, req)
	require.NoError(t, err)
	require.Same(t, req, client.gotReq)
	require.Contains(t, msg, "LIMITED")
	require.Contains(t, msg, "MODERATE")
	require.Contains(t, strings.ToLower(msg), "no restart")
}

// The most likely FIRST command against a fresh pipeline sets one floor. The other side is
// then resolved from the armed default, and the confirmation must say so: a bare "resume=HIGH"
// is indistinguishable from a HIGH a predecessor pinned, and the operator cannot tell which
// half of the pair they actually control. Neither floor may ever render blank.
func TestRunConstructionDeliveryFloors_AnnotatesTheSideResolvedFromTheDefault(t *testing.T) {
	client := &fakeConstructionFloorsClient{resp: &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite: "X1-VB74-I55", BuyFloor: "LIMITED", ResumeFloor: "HIGH",
		BuyFloorIsDefault: false, ResumeFloorIsDefault: true, Changed: true,
	}}

	msg, err := runConstructionDeliveryFloors(context.Background(), client,
		&pb.ConstructionDeliveryFloorsRequest{ConstructionSite: "X1-VB74-I55"})
	require.NoError(t, err)
	require.Contains(t, msg, "buy=LIMITED")
	require.Contains(t, msg, "resume=HIGH (default)",
		"the side resolved from the armed default must be marked as such")
	require.NotContains(t, msg, "buy=LIMITED (default)",
		"an explicitly-set floor must NOT be marked as a default")
}

// The no-op report is read for exactly the same "which of these did I set?" question as a
// successful tune -- more so, since a re-run of a command that changed nothing is usually the
// operator checking. It is the SECOND message path, so the annotation is pinned on it
// independently; sharing a helper today is not a guarantee both paths call it tomorrow.
func TestRunConstructionDeliveryFloors_NoOpReportsUnchanged(t *testing.T) {
	client := &fakeConstructionFloorsClient{resp: &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite: "X1-VB74-I55", BuyFloor: "MODERATE", ResumeFloor: "HIGH",
		BuyFloorIsDefault: true, ResumeFloorIsDefault: false, Changed: false,
	}}
	msg, err := runConstructionDeliveryFloors(context.Background(), client,
		&pb.ConstructionDeliveryFloorsRequest{ConstructionSite: "X1-VB74-I55"})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "unchanged")
	require.Contains(t, msg, "buy=MODERATE (default)",
		"the unchanged path must mark a defaulted floor too, or a re-run tells the operator less than the first run did")
	require.NotContains(t, msg, "resume=HIGH (default)",
		"an explicitly-set floor must NOT be marked as a default on the unchanged path either")
}

// A daemon refusal (a collapsed gap caught by the fail-closed re-check, or no running
// pipeline) must surface to the operator, not be swallowed into a success line.
func TestRunConstructionDeliveryFloors_ErrorPropagates(t *testing.T) {
	client := &fakeConstructionFloorsClient{respErr: errors.New("no active construction pipeline for X1-VB74-I55")}
	_, err := runConstructionDeliveryFloors(context.Background(), client,
		&pb.ConstructionDeliveryFloorsRequest{ConstructionSite: "X1-VB74-I55"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "X1-VB74-I55")
}

// ONE VERB, TWO RPCs (adjudication #5), dispatched on which flags were set. The floors
// have no good; the per-good override requires one. Mixing them in one call is ambiguous,
// so it is rejected rather than silently resolved to one of the two.
func TestConstructionOverrideFlags_DispatchesFloorsAndPerGoodSeparately(t *testing.T) {
	require.True(t, constructionOverrideFlags{buyFloor: "MODERATE"}.anyFloorSet())
	require.True(t, constructionOverrideFlags{resumeFloor: "HIGH"}.anyFloorSet())
	require.False(t, constructionOverrideFlags{minSupply: "LIMITED"}.anyFloorSet())

	require.True(t, constructionOverrideFlags{minSupply: "LIMITED"}.anyKnobSet())
	require.False(t, constructionOverrideFlags{buyFloor: "MODERATE"}.anyKnobSet(),
		"a delivery floor is not a per-good knob; anyKnobSet must not claim it")
}

// A flag the operator cannot type is a knob that does not exist. This asserts the two
// floors are actually REGISTERED on the verb -- the failure mode a builder/runner test
// cannot see, and exactly the silent-no-op class this phase exists to remove.
func TestNewConstructionOverrideCommand_RegistersTheDeliveryFloorFlags(t *testing.T) {
	cmd := newConstructionOverrideCommand()
	for _, name := range []string{"buy-floor", "resume-floor"} {
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, "--%s must be registered on `construction override`", name)
		require.Equal(t, "", flag.DefValue,
			"--%s must default to UNSET; the armed default is resolved by the reader, not stamped by the CLI", name)
	}
}

// A flag that advertises what it refuses costs the operator a round-trip to discover the
// exclusion -- so each floor's help must carry its OWN vocabulary, and it is DERIVED from the
// list the validator rejects against rather than retyped beside it. This pins the derivation
// against the realistic regression: someone re-hardcodes the help, it is correct that day, and
// a later vocabulary change desyncs it silently.
//
// The excluded ladder end is asserted by COUNT, not absence: the help names it deliberately in
// the prose clause that explains why it is gone ("not ABUNDANT: nothing sits strictly above
// it"), so a bare NotContains would fail on the very sentence that makes the help useful.
// Exactly once means named as excluded and NOT present in the vocabulary -- promote a ladder
// end into its own flag's accepted set and the count goes to two.
func TestNewConstructionOverrideCommand_FloorHelpAdvertisesOnlyItsOwnVocabulary(t *testing.T) {
	cmd := newConstructionOverrideCommand()

	for _, tc := range []struct {
		flagName string
		accepts  []manufacturing.SupplyLevel
		excluded manufacturing.SupplyLevel
	}{
		{"buy-floor", validBuyFloors, manufacturing.SupplyLevelAbundant},
		{"resume-floor", validResumeFloors, manufacturing.SupplyLevelScarce},
	} {
		flag := cmd.Flags().Lookup(tc.flagName)
		require.NotNil(t, flag, "--%s must be registered", tc.flagName)
		require.NotEmpty(t, tc.accepts,
			"--%s vocabulary is empty, so the loop below would assert nothing", tc.flagName)

		for _, level := range tc.accepts {
			require.Contains(t, flag.Usage, string(level),
				"--%s accepts %s, so its help must advertise it", tc.flagName, level)
		}

		require.Equal(t, 1, strings.Count(flag.Usage, string(tc.excluded)),
			"--%s must name %s exactly once -- in the clause explaining its exclusion, never in the accepted vocabulary",
			tc.flagName, tc.excluded)
		require.Contains(t, flag.Usage, "not "+string(tc.excluded),
			"--%s must say WHY %s is unavailable, not merely omit it", tc.flagName, tc.excluded)
	}
}
