package steps

import (
	"context"
	"fmt"
	"strings"

	appgoods "github.com/andrescamacho/spacetraders-go/internal/application/goods"
	"github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
	"github.com/cucumber/godog"
)

type supplyChainResolverContext struct {
	resolver          *services.SupplyChainResolver
	supplyChainMap    map[string][]string
	mockMarketRepo    *helpers.MockMarketRepository
	dependencyTree    *goods.SupplyChainNode
	buildError        error
	validationError   error
	playerID          int
	systemSymbol      string
}

func (ctx *supplyChainResolverContext) reset() {
	ctx.supplyChainMap = make(map[string][]string)
	ctx.mockMarketRepo = helpers.NewMockMarketRepository()
	ctx.resolver = services.NewSupplyChainResolver(ctx.supplyChainMap, ctx.mockMarketRepo)
	ctx.dependencyTree = nil
	ctx.buildError = nil
	ctx.validationError = nil
	ctx.playerID = 1
	ctx.systemSymbol = "X1"
}

// ============================================================================
// Setup Steps
// ============================================================================

func (ctx *supplyChainResolverContext) aSupplyChainMap() error {
	// Use the default supply chain map
	ctx.supplyChainMap = appgoods.ExportToImportMap
	ctx.resolver = services.NewSupplyChainResolver(ctx.supplyChainMap, ctx.mockMarketRepo)
	return nil
}

func (ctx *supplyChainResolverContext) aSupplyChainMapWithRequiring(output, inputs string) error {
	ctx.supplyChainMap[output] = strings.Split(inputs, ", ")
	ctx.resolver = services.NewSupplyChainResolver(ctx.supplyChainMap, ctx.mockMarketRepo)
	return nil
}

func (ctx *supplyChainResolverContext) aSupplyChainMapWith(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		output := row.Cells[0].Value
		inputsStr := row.Cells[1].Value
		if inputsStr != "" {
			ctx.supplyChainMap[output] = strings.Split(inputsStr, ", ")
		}
	}
	ctx.resolver = services.NewSupplyChainResolver(ctx.supplyChainMap, ctx.mockMarketRepo)
	return nil
}

func (ctx *supplyChainResolverContext) anEmptySupplyChainMap() error {
	ctx.supplyChainMap = make(map[string][]string)
	ctx.resolver = services.NewSupplyChainResolver(ctx.supplyChainMap, ctx.mockMarketRepo)
	return nil
}

func (ctx *supplyChainResolverContext) marketSellsWithActivityAndSupply(
	marketSymbol, goodSymbol, activity, supply string,
) error {
	return ctx.mockMarketRepo.AddMarketSellingGood(marketSymbol, goodSymbol, activity, supply, 100)
}

func (ctx *supplyChainResolverContext) marketAtWaypointSellsWithActivityAndSupplyAtPrice(
	marketSymbol, waypointSymbol, goodSymbol, activity, supply string, price int,
) error {
	return ctx.mockMarketRepo.AddMarketSellingGoodAtWaypoint(
		marketSymbol, waypointSymbol, goodSymbol, activity, supply, price,
	)
}

func (ctx *supplyChainResolverContext) isNotAvailableInAnyMarket(goodSymbol string) error {
	// Nothing to do - good is simply not added to mock repo
	return nil
}

func (ctx *supplyChainResolverContext) isNotInTheSupplyChainMap(goodSymbol string) error {
	// Verify it's not in the map
	if _, exists := ctx.supplyChainMap[goodSymbol]; exists {
		return fmt.Errorf("good %s should not be in supply chain map", goodSymbol)
	}
	return nil
}

// ============================================================================
// Action Steps
// ============================================================================

func (ctx *supplyChainResolverContext) iBuildDependencyTreeForInSystem(
	goodSymbol, systemSymbol string,
) error {
	tree, err := ctx.resolver.BuildDependencyTree(
		context.Background(),
		goodSymbol,
		systemSymbol,
		ctx.playerID,
	)
	ctx.dependencyTree = tree
	ctx.buildError = err
	return nil
}

func (ctx *supplyChainResolverContext) iAttemptToBuildDependencyTreeForInSystem(
	goodSymbol, systemSymbol string,
) error {
	return ctx.iBuildDependencyTreeForInSystem(goodSymbol, systemSymbol)
}

func (ctx *supplyChainResolverContext) iValidateTheSupplyChainFor(goodSymbol string) error {
	ctx.validationError = ctx.resolver.ValidateChain(goodSymbol)
	return nil
}

// ============================================================================
// Assertion Steps
// ============================================================================

func (ctx *supplyChainResolverContext) theTreeShouldHaveRootWithAcquisitionMethod(
	goodSymbol, method string,
) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	if ctx.dependencyTree.Good != goodSymbol {
		return fmt.Errorf("expected root good %s, got %s", goodSymbol, ctx.dependencyTree.Good)
	}
	expectedMethod := goods.AcquisitionMethod(method)
	if ctx.dependencyTree.AcquisitionMethod != expectedMethod {
		return fmt.Errorf("expected acquisition method %s, got %s",
			method, ctx.dependencyTree.AcquisitionMethod)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theRootShouldHaveChildren(count int) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	actual := len(ctx.dependencyTree.Children)
	if actual != count {
		return fmt.Errorf("expected %d children, got %d", count, actual)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theRootMarketActivityShouldBe(activity string) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	if ctx.dependencyTree.MarketActivity != activity {
		return fmt.Errorf("expected market activity %s, got %s",
			activity, ctx.dependencyTree.MarketActivity)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theRootSupplyLevelShouldBe(supply string) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	if ctx.dependencyTree.SupplyLevel != supply {
		return fmt.Errorf("expected supply level %s, got %s",
			supply, ctx.dependencyTree.SupplyLevel)
	}
	return nil
}

func (ctx *supplyChainResolverContext) childShouldBeWithAcquisitionMethod(
	index int, goodSymbol, method string,
) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	if index >= len(ctx.dependencyTree.Children) {
		return fmt.Errorf("child index %d out of range (have %d children)",
			index, len(ctx.dependencyTree.Children))
	}
	child := ctx.dependencyTree.Children[index]
	if child.Good != goodSymbol {
		return fmt.Errorf("expected child %d good %s, got %s", index, goodSymbol, child.Good)
	}
	expectedMethod := goods.AcquisitionMethod(method)
	if child.AcquisitionMethod != expectedMethod {
		return fmt.Errorf("expected child %d acquisition method %s, got %s",
			index, method, child.AcquisitionMethod)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theTreeShouldContainNodeWithAcquisitionMethod(
	goodSymbol, method string,
) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	nodes := ctx.dependencyTree.FlattenToList()
	for _, node := range nodes {
		if node.Good == goodSymbol {
			expectedMethod := goods.AcquisitionMethod(method)
			if node.AcquisitionMethod != expectedMethod {
				return fmt.Errorf("node %s has acquisition method %s, expected %s",
					goodSymbol, node.AcquisitionMethod, method)
			}
			return nil
		}
	}
	return fmt.Errorf("tree does not contain node %s", goodSymbol)
}

func (ctx *supplyChainResolverContext) theTreeDepthShouldBe(depth int) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	actual := ctx.dependencyTree.TotalDepth()
	if actual != depth {
		return fmt.Errorf("expected tree depth %d, got %d", depth, actual)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theTreeShouldHaveTotalNodes(count int) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	actual := ctx.dependencyTree.CountNodes()
	if actual != count {
		return fmt.Errorf("expected %d total nodes, got %d", count, actual)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theTreeShouldContainBUYNodes(count int) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	buyCount, _ := ctx.dependencyTree.CountByAcquisitionMethod()
	if buyCount != count {
		return fmt.Errorf("expected %d BUY nodes, got %d", count, buyCount)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theTreeShouldContainFABRICATENodes(count int) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	_, fabricateCount := ctx.dependencyTree.CountByAcquisitionMethod()
	if fabricateCount != count {
		return fmt.Errorf("expected %d FABRICATE nodes, got %d", count, fabricateCount)
	}
	return nil
}

func (ctx *supplyChainResolverContext) treeBuildingShouldFailWithCircularDependencyError() error {
	if ctx.buildError == nil {
		return fmt.Errorf("expected circular dependency error, got no error")
	}
	if _, ok := ctx.buildError.(*goods.ErrCircularDependency); !ok {
		return fmt.Errorf("expected circular dependency error, got: %v", ctx.buildError)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theErrorShouldMentionGoods(good1, good2 string) error {
	if ctx.buildError == nil {
		return fmt.Errorf("no error to check")
	}
	errorMsg := ctx.buildError.Error()
	if !strings.Contains(errorMsg, good1) || !strings.Contains(errorMsg, good2) {
		return fmt.Errorf("error should mention goods %s and %s, got: %s", good1, good2, errorMsg)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theCyclePathShouldBe(expectedPath string) error {
	if ctx.buildError == nil {
		return fmt.Errorf("no error to check")
	}
	cyclicErr, ok := ctx.buildError.(*goods.ErrCircularDependency)
	if !ok {
		return fmt.Errorf("not a circular dependency error")
	}
	actualPath := strings.Join(cyclicErr.Chain, " -> ")
	if actualPath != expectedPath {
		return fmt.Errorf("expected cycle path %s, got %s", expectedPath, actualPath)
	}
	return nil
}

func (ctx *supplyChainResolverContext) treeBuildingShouldFailWithUnknownGoodError() error {
	if ctx.buildError == nil {
		return fmt.Errorf("expected unknown good error, got no error")
	}
	if _, ok := ctx.buildError.(*goods.ErrUnknownGood); !ok {
		return fmt.Errorf("expected unknown good error, got: %v", ctx.buildError)
	}
	return nil
}

func (ctx *supplyChainResolverContext) theErrorShouldMention(goodSymbol string) error {
	if ctx.buildError == nil {
		return fmt.Errorf("no error to check")
	}
	errorMsg := ctx.buildError.Error()
	if !strings.Contains(errorMsg, goodSymbol) {
		return fmt.Errorf("error should mention %s, got: %s", goodSymbol, errorMsg)
	}
	return nil
}

func (ctx *supplyChainResolverContext) validationShouldSucceed() error {
	if ctx.validationError != nil {
		return fmt.Errorf("expected validation to succeed, got error: %v", ctx.validationError)
	}
	return nil
}

func (ctx *supplyChainResolverContext) nodeShouldHaveWaypointSymbol(goodSymbol, waypointSymbol string) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	nodes := ctx.dependencyTree.FlattenToList()
	for _, node := range nodes {
		if node.Good == goodSymbol {
			if node.WaypointSymbol != waypointSymbol {
				return fmt.Errorf("node %s has waypoint %s, expected %s",
					goodSymbol, node.WaypointSymbol, waypointSymbol)
			}
			return nil
		}
	}
	return fmt.Errorf("tree does not contain node %s", goodSymbol)
}

func (ctx *supplyChainResolverContext) nodeShouldHaveMarketActivity(goodSymbol, activity string) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	nodes := ctx.dependencyTree.FlattenToList()
	for _, node := range nodes {
		if node.Good == goodSymbol {
			if node.MarketActivity != activity {
				return fmt.Errorf("node %s has market activity %s, expected %s",
					goodSymbol, node.MarketActivity, activity)
			}
			return nil
		}
	}
	return fmt.Errorf("tree does not contain node %s", goodSymbol)
}

func (ctx *supplyChainResolverContext) nodeShouldHaveSupplyLevel(goodSymbol, supply string) error {
	if ctx.dependencyTree == nil {
		return fmt.Errorf("dependency tree is nil")
	}
	nodes := ctx.dependencyTree.FlattenToList()
	for _, node := range nodes {
		if node.Good == goodSymbol {
			if node.SupplyLevel != supply {
				return fmt.Errorf("node %s has supply level %s, expected %s",
					goodSymbol, node.SupplyLevel, supply)
			}
			return nil
		}
	}
	return fmt.Errorf("tree does not contain node %s", goodSymbol)
}

func (ctx *supplyChainResolverContext) nodeShouldHaveEmptyMarketActivity(goodSymbol string) error {
	return ctx.nodeShouldHaveMarketActivity(goodSymbol, "")
}

func (ctx *supplyChainResolverContext) nodeShouldHaveEmptySupplyLevel(goodSymbol string) error {
	return ctx.nodeShouldHaveSupplyLevel(goodSymbol, "")
}

// ============================================================================
// Registration
// ============================================================================

// RegisterSupplyChainResolverSteps registers all supply chain resolver step definitions
func RegisterSupplyChainResolverSteps(sc *godog.ScenarioContext) {
	ctx := &supplyChainResolverContext{}
	sc.Before(func(bddCtx context.Context, sc *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return bddCtx, nil
	})

	// Setup steps
	sc.Step(`^a supply chain map$`, ctx.aSupplyChainMap)
	sc.Step(`^a supply chain map with "([^"]*)" requiring "([^"]*)"$`, ctx.aSupplyChainMapWithRequiring)
	sc.Step(`^a supply chain map with:$`, ctx.aSupplyChainMapWith)
	sc.Step(`^an empty supply chain map$`, ctx.anEmptySupplyChainMap)
	sc.Step(`^market "([^"]*)" sells "([^"]*)" with activity "([^"]*)" and supply "([^"]*)"$`,
		ctx.marketSellsWithActivityAndSupply)
	sc.Step(`^market "([^"]*)" at waypoint "([^"]*)" sells "([^"]*)" with activity "([^"]*)" and supply "([^"]*)" at price (\d+)$`,
		ctx.marketAtWaypointSellsWithActivityAndSupplyAtPrice)
	sc.Step(`^"([^"]*)" is not available in any market$`, ctx.isNotAvailableInAnyMarket)
	sc.Step(`^"([^"]*)" is not in the supply chain map$`, ctx.isNotInTheSupplyChainMap)

	// Action steps
	sc.Step(`^I build dependency tree for "([^"]*)" in system "([^"]*)"$`,
		ctx.iBuildDependencyTreeForInSystem)
	sc.Step(`^I attempt to build dependency tree for "([^"]*)" in system "([^"]*)"$`,
		ctx.iAttemptToBuildDependencyTreeForInSystem)
	sc.Step(`^I validate the supply chain for "([^"]*)"$`, ctx.iValidateTheSupplyChainFor)

	// Assertion steps
	sc.Step(`^the tree should have root "([^"]*)" with acquisition method "([^"]*)"$`,
		ctx.theTreeShouldHaveRootWithAcquisitionMethod)
	sc.Step(`^the root should have (\d+) children$`, ctx.theRootShouldHaveChildren)
	sc.Step(`^the root market activity should be "([^"]*)"$`, ctx.theRootMarketActivityShouldBe)
	sc.Step(`^the root supply level should be "([^"]*)"$`, ctx.theRootSupplyLevelShouldBe)
	sc.Step(`^child (\d+) should be "([^"]*)" with acquisition method "([^"]*)"$`,
		ctx.childShouldBeWithAcquisitionMethod)
	sc.Step(`^the tree should contain node "([^"]*)" with acquisition method "([^"]*)"$`,
		ctx.theTreeShouldContainNodeWithAcquisitionMethod)
	sc.Step(`^the tree depth should be (\d+)$`, ctx.theTreeDepthShouldBe)
	sc.Step(`^the tree should have (\d+) total nodes$`, ctx.theTreeShouldHaveTotalNodes)
	sc.Step(`^the tree should contain (\d+) BUY nodes$`, ctx.theTreeShouldContainBUYNodes)
	sc.Step(`^the tree should contain (\d+) FABRICATE nodes$`, ctx.theTreeShouldContainFABRICATENodes)
	sc.Step(`^tree building should fail with circular dependency error$`,
		ctx.treeBuildingShouldFailWithCircularDependencyError)
	sc.Step(`^the error should mention goods "([^"]*)" and "([^"]*)"$`, ctx.theErrorShouldMentionGoods)
	sc.Step(`^the cycle path should be "([^"]*)"$`, ctx.theCyclePathShouldBe)
	sc.Step(`^tree building should fail with unknown good error$`,
		ctx.treeBuildingShouldFailWithUnknownGoodError)
	sc.Step(`^the error should mention "([^"]*)"$`, ctx.theErrorShouldMention)
	sc.Step(`^validation should succeed$`, ctx.validationShouldSucceed)
	sc.Step(`^node "([^"]*)" should have waypoint symbol "([^"]*)"$`,
		ctx.nodeShouldHaveWaypointSymbol)
	sc.Step(`^node "([^"]*)" should have market activity "([^"]*)"$`,
		ctx.nodeShouldHaveMarketActivity)
	sc.Step(`^node "([^"]*)" should have supply level "([^"]*)"$`,
		ctx.nodeShouldHaveSupplyLevel)
	sc.Step(`^node "([^"]*)" should have empty market activity$`,
		ctx.nodeShouldHaveEmptyMarketActivity)
	sc.Step(`^node "([^"]*)" should have empty supply level$`,
		ctx.nodeShouldHaveEmptySupplyLevel)
}
