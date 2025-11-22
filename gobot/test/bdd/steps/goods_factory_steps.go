package steps

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type goodsFactoryContext struct {
	// GoodsFactory context
	playerID         int
	targetGood       string
	systemSymbol     string
	dependencyTree   *goods.SupplyChainNode
	metadata         map[string]interface{}
	factory          *goods.GoodsFactory
	factoryErr       error
	clock            *shared.MockClock

	// SupplyChainNode context
	node              *goods.SupplyChainNode
	nodeList          []*goods.SupplyChainNode
	rawMaterials      []string
	treeDepth         int
	nodeCount         int
	buyCount          int
	fabricateCount    int
	allChildrenDone   bool
	estimatedDuration time.Duration

	// Common test state
	intResult         int
	boolResult        bool
	errorResult       error
}

func (gfc *goodsFactoryContext) reset() {
	gfc.playerID = 0
	gfc.targetGood = ""
	gfc.systemSymbol = ""
	gfc.dependencyTree = nil
	gfc.metadata = make(map[string]interface{})
	gfc.factory = nil
	gfc.factoryErr = nil
	gfc.clock = shared.NewMockClock(time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC))

	gfc.node = nil
	gfc.nodeList = nil
	gfc.rawMaterials = nil
	gfc.treeDepth = 0
	gfc.nodeCount = 0
	gfc.buyCount = 0
	gfc.fabricateCount = 0
	gfc.allChildrenDone = false
	gfc.estimatedDuration = 0

	gfc.intResult = 0
	gfc.boolResult = false
	gfc.errorResult = nil
}

// ============================================================================
// GoodsFactory Setup Steps
// ============================================================================

func (gfc *goodsFactoryContext) aGoodsFactoryForPlayerProducingInSystem(playerID int, targetGood, systemSymbol string) error {
	gfc.playerID = playerID
	gfc.targetGood = targetGood
	gfc.systemSymbol = systemSymbol
	return nil
}

func (gfc *goodsFactoryContext) aDependencyTreeWithRoot(rootGood string) error {
	gfc.dependencyTree = goods.NewSupplyChainNode(rootGood, goods.AcquisitionFabricate)
	return nil
}

func (gfc *goodsFactoryContext) factoryMetadata(table *godog.Table) error {
	gfc.metadata = make(map[string]interface{})
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		key := row.Cells[0].Value
		value := row.Cells[1].Value
		// Try to convert to int, otherwise store as string
		if intVal, err := strconv.Atoi(value); err == nil {
			gfc.metadata[key] = intVal
		} else {
			gfc.metadata[key] = value
		}
	}
	return nil
}

func (gfc *goodsFactoryContext) aGoodsFactoryInState(status string) error {
	gfc.playerID = 1
	gfc.targetGood = "TEST_GOOD"
	gfc.systemSymbol = "X1-TEST"
	gfc.dependencyTree = goods.NewSupplyChainNode("TEST_GOOD", goods.AcquisitionBuy)
	gfc.metadata = make(map[string]interface{})

	gfc.factory = goods.NewGoodsFactory(
		"test-factory-1",
		gfc.playerID,
		gfc.targetGood,
		gfc.systemSymbol,
		gfc.dependencyTree,
		gfc.metadata,
		gfc.clock,
	)

	// Transition to desired state
	switch status {
	case "PENDING":
		// Already in PENDING
	case "ACTIVE":
		_ = gfc.factory.Start()
	case "COMPLETED":
		_ = gfc.factory.Start()
		_ = gfc.factory.Complete()
	case "FAILED":
		_ = gfc.factory.Start()
		_ = gfc.factory.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		_ = gfc.factory.Start()
		_ = gfc.factory.Stop()
	}

	return nil
}

func (gfc *goodsFactoryContext) aGoodsFactoryWithDependencyTreeOfNodes(nodeCount int) error {
	// Create a simple linear tree for testing
	gfc.dependencyTree = goods.NewSupplyChainNode("ROOT", goods.AcquisitionFabricate)
	current := gfc.dependencyTree
	for i := 1; i < nodeCount; i++ {
		child := goods.NewSupplyChainNode(fmt.Sprintf("NODE_%d", i), goods.AcquisitionBuy)
		current.AddChild(child)
		current = child
	}

	gfc.factory = goods.NewGoodsFactory(
		"test-factory-1",
		1,
		"ROOT",
		"X1-TEST",
		gfc.dependencyTree,
		make(map[string]interface{}),
		gfc.clock,
	)

	return nil
}

func (gfc *goodsFactoryContext) nodesAreCompleted(count int) error {
	// Mark the first N nodes as completed
	nodes := gfc.dependencyTree.FlattenToList()
	for i := 0; i < count && i < len(nodes); i++ {
		nodes[i].MarkCompleted(10)
	}
	return nil
}

// ============================================================================
// GoodsFactory Action Steps
// ============================================================================

func (gfc *goodsFactoryContext) iCreateTheGoodsFactory() error {
	gfc.factory = goods.NewGoodsFactory(
		"test-factory-1",
		gfc.playerID,
		gfc.targetGood,
		gfc.systemSymbol,
		gfc.dependencyTree,
		gfc.metadata,
		gfc.clock,
	)
	return nil
}

func (gfc *goodsFactoryContext) iStartTheFactory() error {
	gfc.factoryErr = gfc.factory.Start()
	return nil
}

func (gfc *goodsFactoryContext) iAttemptToStartTheFactory() error {
	return gfc.iStartTheFactory()
}

func (gfc *goodsFactoryContext) iCompleteTheFactory() error {
	gfc.factoryErr = gfc.factory.Complete()
	return nil
}

func (gfc *goodsFactoryContext) iAttemptToCompleteTheFactory() error {
	return gfc.iCompleteTheFactory()
}

func (gfc *goodsFactoryContext) iFailTheFactoryWithError(errorMsg string) error {
	gfc.factoryErr = gfc.factory.Fail(fmt.Errorf("%s", errorMsg))
	return nil
}

func (gfc *goodsFactoryContext) iAttemptToFailTheFactory() error {
	return gfc.iFailTheFactoryWithError("test error")
}

func (gfc *goodsFactoryContext) iStopTheFactory() error {
	gfc.factoryErr = gfc.factory.Stop()
	return nil
}

func (gfc *goodsFactoryContext) iAttemptToStopTheFactory() error {
	return gfc.iStopTheFactory()
}

func (gfc *goodsFactoryContext) iTransitionFactoryToState(state string) error {
	switch state {
	case "ACTIVE":
		return gfc.factory.Start()
	case "COMPLETED":
		_ = gfc.factory.Start()
		return gfc.factory.Complete()
	case "FAILED":
		_ = gfc.factory.Start()
		return gfc.factory.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		_ = gfc.factory.Start()
		return gfc.factory.Stop()
	}
	return nil
}

func (gfc *goodsFactoryContext) iSetQuantityAcquiredTo(quantity int) error {
	gfc.factory.SetQuantityAcquired(quantity)
	return nil
}

func (gfc *goodsFactoryContext) iAddCostOfCredits(cost int) error {
	gfc.factory.AddCost(cost)
	return nil
}

func (gfc *goodsFactoryContext) iUpdateMetadataWith(table *godog.Table) error {
	updates := make(map[string]interface{})
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		key := row.Cells[0].Value
		value := row.Cells[1].Value
		// Try to convert to int, otherwise store as string
		if intVal, err := strconv.Atoi(value); err == nil {
			updates[key] = intVal
		} else {
			updates[key] = value
		}
	}
	gfc.factory.UpdateMetadata(updates)
	return nil
}

func (gfc *goodsFactoryContext) iGetTheFactoryProgress() error {
	gfc.intResult = gfc.factory.Progress()
	return nil
}

// ============================================================================
// GoodsFactory Assertion Steps
// ============================================================================

func (gfc *goodsFactoryContext) theGoodsFactoryShouldBeValid() error {
	if gfc.factory == nil {
		return fmt.Errorf("expected factory to be created, got nil")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryPlayerIDShouldBe(expected int) error {
	if gfc.factory.PlayerID() != expected {
		return fmt.Errorf("expected player ID %d, got %d", expected, gfc.factory.PlayerID())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryTargetGoodShouldBe(expected string) error {
	if gfc.factory.TargetGood() != expected {
		return fmt.Errorf("expected target good '%s', got '%s'", expected, gfc.factory.TargetGood())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactorySystemSymbolShouldBe(expected string) error {
	if gfc.factory.SystemSymbol() != expected {
		return fmt.Errorf("expected system symbol '%s', got '%s'", expected, gfc.factory.SystemSymbol())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryStatusShouldBe(expected string) error {
	actual := string(gfc.factory.Status())
	if actual != expected {
		return fmt.Errorf("expected status '%s', got '%s'", expected, actual)
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryStartedAtTimestampShouldBeSet() error {
	if gfc.factory.StartedAt() == nil {
		return fmt.Errorf("expected started_at to be set, got nil")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryStoppedAtTimestampShouldBeSet() error {
	if gfc.factory.StoppedAt() == nil {
		return fmt.Errorf("expected stopped_at to be set, got nil")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryLastErrorShouldBe(expectedMsg string) error {
	if gfc.factory.LastError() == nil {
		return fmt.Errorf("expected last error '%s', got nil", expectedMsg)
	}
	if gfc.factory.LastError().Error() != expectedMsg {
		return fmt.Errorf("expected last error '%s', got '%s'", expectedMsg, gfc.factory.LastError().Error())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryShouldBeActive() error {
	if !gfc.factory.IsActive() {
		return fmt.Errorf("expected factory to be active, got status %s", gfc.factory.Status())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryShouldBeFinished() error {
	if !gfc.factory.IsFinished() {
		return fmt.Errorf("expected factory to be finished, got status %s", gfc.factory.Status())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryStartShouldFailWithError(expectedError string) error {
	if gfc.factoryErr == nil {
		return fmt.Errorf("expected error '%s', but operation succeeded", expectedError)
	}
	if gfc.factoryErr.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, gfc.factoryErr.Error())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryCompleteShouldFailWithError(expectedError string) error {
	return gfc.theFactoryStartShouldFailWithError(expectedError)
}

func (gfc *goodsFactoryContext) theFactoryFailShouldFailWithError(expectedError string) error {
	return gfc.theFactoryStartShouldFailWithError(expectedError)
}

func (gfc *goodsFactoryContext) theFactoryStopShouldFailWithError(expectedError string) error {
	return gfc.theFactoryStartShouldFailWithError(expectedError)
}

func (gfc *goodsFactoryContext) theFactoryCanBeStarted() error {
	if !gfc.factory.CanStart() {
		return fmt.Errorf("expected factory to be startable, but CanStart() returned false")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryCannotBeStarted() error {
	if gfc.factory.CanStart() {
		return fmt.Errorf("expected factory not to be startable, but CanStart() returned true")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryCanBeCompleted() error {
	if !gfc.factory.CanComplete() {
		return fmt.Errorf("expected factory to be completable, but CanComplete() returned false")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryCannotBeCompleted() error {
	if gfc.factory.CanComplete() {
		return fmt.Errorf("expected factory not to be completable, but CanComplete() returned true")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryCanBeFailed() error {
	if !gfc.factory.CanFail() {
		return fmt.Errorf("expected factory to be failable, but CanFail() returned false")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryCannotBeFailed() error {
	if gfc.factory.CanFail() {
		return fmt.Errorf("expected factory not to be failable, but CanFail() returned true")
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryMetadataShouldContainWithValue(key, value string) error {
	actual, exists := gfc.factory.GetMetadataValue(key)
	if !exists {
		return fmt.Errorf("expected metadata to contain key '%s', but it doesn't exist", key)
	}

	// Try to compare as int or string
	if intVal, err := strconv.Atoi(value); err == nil {
		if actual != intVal {
			return fmt.Errorf("expected metadata[%s] to be %d, got %v", key, intVal, actual)
		}
	} else {
		if actual != value {
			return fmt.Errorf("expected metadata[%s] to be '%s', got '%v'", key, value, actual)
		}
	}

	return nil
}

func (gfc *goodsFactoryContext) theFactoryQuantityAcquiredShouldBe(expected int) error {
	if gfc.factory.QuantityAcquired() != expected {
		return fmt.Errorf("expected quantity acquired %d, got %d", expected, gfc.factory.QuantityAcquired())
	}
	return nil
}

func (gfc *goodsFactoryContext) theFactoryTotalCostShouldBe(expected int) error {
	if gfc.factory.TotalCost() != expected {
		return fmt.Errorf("expected total cost %d, got %d", expected, gfc.factory.TotalCost())
	}
	return nil
}

func (gfc *goodsFactoryContext) theProgressShouldBePercent(expected int) error {
	if gfc.intResult != expected {
		return fmt.Errorf("expected progress %d%%, got %d%%", expected, gfc.intResult)
	}
	return nil
}

// ============================================================================
// SupplyChainNode Setup Steps
// ============================================================================

func (gfc *goodsFactoryContext) aSupplyChainNodeForGoodWithAcquisitionMethod(goodSymbol, method string) error {
	var acquisitionMethod goods.AcquisitionMethod
	if method == "BUY" {
		acquisitionMethod = goods.AcquisitionBuy
	} else {
		acquisitionMethod = goods.AcquisitionFabricate
	}
	gfc.node = goods.NewSupplyChainNode(goodSymbol, acquisitionMethod)
	return nil
}

func (gfc *goodsFactoryContext) theNodeHasChildWithAcquisitionMethod(childGood, method string) error {
	var acquisitionMethod goods.AcquisitionMethod
	if method == "BUY" {
		acquisitionMethod = goods.AcquisitionBuy
	} else {
		acquisitionMethod = goods.AcquisitionFabricate
	}
	child := goods.NewSupplyChainNode(childGood, acquisitionMethod)
	gfc.node.AddChild(child)
	return nil
}

func (gfc *goodsFactoryContext) theNodeHasMarketActivity(activity string) error {
	gfc.node.MarketActivity = activity
	return nil
}

func (gfc *goodsFactoryContext) theNodeHasSupplyLevel(supply string) error {
	gfc.node.SupplyLevel = supply
	return nil
}

func (gfc *goodsFactoryContext) aLeafNodeForGood(goodSymbol string) error {
	gfc.node = goods.NewSupplyChainNode(goodSymbol, goods.AcquisitionBuy)
	return nil
}

func (gfc *goodsFactoryContext) aSupplyChainTree(table *godog.Table) error {
	// Build tree from table representation
	// First pass: create all nodes
	nodeMap := make(map[string]*goods.SupplyChainNode)
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		goodSymbol := row.Cells[0].Value
		childrenStr := ""
		method := goods.AcquisitionFabricate

		// Check if we have method column (3 columns) or just children (2 columns)
		if len(row.Cells) >= 3 {
			methodStr := row.Cells[1].Value
			if methodStr == "BUY" {
				method = goods.AcquisitionBuy
			}
			childrenStr = row.Cells[2].Value
		} else {
			childrenStr = row.Cells[1].Value
			// Infer method from whether it has children
			if childrenStr == "" {
				method = goods.AcquisitionBuy
			}
		}

		node := goods.NewSupplyChainNode(goodSymbol, method)
		nodeMap[goodSymbol] = node

		if i == 1 {
			gfc.node = node // First node is root
		}
	}

	// Second pass: connect children
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		goodSymbol := row.Cells[0].Value
		childrenStr := ""

		if len(row.Cells) >= 3 {
			childrenStr = row.Cells[2].Value
		} else {
			childrenStr = row.Cells[1].Value
		}

		if childrenStr != "" {
			parent := nodeMap[goodSymbol]
			children := splitChildren(childrenStr)
			for _, childName := range children {
				if child, exists := nodeMap[childName]; exists {
					parent.AddChild(child)
				}
			}
		}
	}

	return nil
}

func splitChildren(childrenStr string) []string {
	if childrenStr == "" {
		return []string{}
	}
	result := []string{}
	current := ""
	for _, ch := range childrenStr {
		if ch == ',' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func (gfc *goodsFactoryContext) aSupplyChainTreeWithDepth(depth int) error {
	// Create a linear tree of the specified depth
	gfc.node = goods.NewSupplyChainNode("ROOT", goods.AcquisitionFabricate)
	current := gfc.node
	for i := 1; i < depth; i++ {
		child := goods.NewSupplyChainNode(fmt.Sprintf("NODE_%d", i), goods.AcquisitionBuy)
		current.AddChild(child)
		current = child
	}
	return nil
}

func (gfc *goodsFactoryContext) theRootNodeHasMarketActivity(activity string) error {
	gfc.node.MarketActivity = activity
	return nil
}

func (gfc *goodsFactoryContext) nodeIsMarkedCompleted(nodeName string) error {
	// Find the node in the tree and mark it completed
	nodes := gfc.node.FlattenToList()
	for _, n := range nodes {
		if n.Good == nodeName {
			n.MarkCompleted(10)
			return nil
		}
	}
	return fmt.Errorf("node '%s' not found in tree", nodeName)
}

// ============================================================================
// SupplyChainNode Action Steps
// ============================================================================

func (gfc *goodsFactoryContext) iCreateTheSupplyChainNode() error {
	// Node is already created in setup steps
	return nil
}

func (gfc *goodsFactoryContext) iCalculateTheTreeDepth() error {
	gfc.treeDepth = gfc.node.TotalDepth()
	return nil
}

func (gfc *goodsFactoryContext) iFlattenTheTreeToAList() error {
	gfc.nodeList = gfc.node.FlattenToList()
	return nil
}

func (gfc *goodsFactoryContext) iGetRequiredRawMaterials() error {
	gfc.rawMaterials = gfc.node.RequiredRawMaterials()
	return nil
}

func (gfc *goodsFactoryContext) iCountTheNodes() error {
	gfc.nodeCount = gfc.node.CountNodes()
	return nil
}

func (gfc *goodsFactoryContext) iCountByAcquisitionMethod() error {
	gfc.buyCount, gfc.fabricateCount = gfc.node.CountByAcquisitionMethod()
	return nil
}

func (gfc *goodsFactoryContext) iMarkTheNodeCompletedWithQuantity(quantity int) error {
	gfc.node.MarkCompleted(quantity)
	return nil
}

func (gfc *goodsFactoryContext) iCheckIfAllChildrenOfAreCompleted(nodeName string) error {
	// Find the node and check
	nodes := gfc.node.FlattenToList()
	for _, n := range nodes {
		if n.Good == nodeName {
			gfc.allChildrenDone = n.AllChildrenCompleted()
			return nil
		}
	}
	return fmt.Errorf("node '%s' not found in tree", nodeName)
}

func (gfc *goodsFactoryContext) iEstimateProductionTime() error {
	gfc.estimatedDuration = gfc.node.EstimateProductionTime()
	return nil
}

// ============================================================================
// SupplyChainNode Assertion Steps
// ============================================================================

func (gfc *goodsFactoryContext) theNodeShouldBeValid() error {
	if gfc.node == nil {
		return fmt.Errorf("expected node to be created, got nil")
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeGoodShouldBe(expected string) error {
	if gfc.node.Good != expected {
		return fmt.Errorf("expected node good '%s', got '%s'", expected, gfc.node.Good)
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeAcquisitionMethodShouldBe(expected string) error {
	actual := string(gfc.node.AcquisitionMethod)
	if actual != expected {
		return fmt.Errorf("expected acquisition method '%s', got '%s'", expected, actual)
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeShouldBeALeaf() error {
	if !gfc.node.IsLeaf() {
		return fmt.Errorf("expected node to be a leaf, but it has %d children", len(gfc.node.Children))
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeShouldNotBeALeaf() error {
	if gfc.node.IsLeaf() {
		return fmt.Errorf("expected node not to be a leaf, but it has no children")
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeShouldHaveChildren(expected int) error {
	actual := len(gfc.node.Children)
	if actual != expected {
		return fmt.Errorf("expected %d children, got %d", expected, actual)
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeMarketActivityShouldBe(expected string) error {
	if gfc.node.MarketActivity != expected {
		return fmt.Errorf("expected market activity '%s', got '%s'", expected, gfc.node.MarketActivity)
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeSupplyLevelShouldBe(expected string) error {
	if gfc.node.SupplyLevel != expected {
		return fmt.Errorf("expected supply level '%s', got '%s'", expected, gfc.node.SupplyLevel)
	}
	return nil
}

func (gfc *goodsFactoryContext) theDepthShouldBe(expected int) error {
	if gfc.treeDepth != expected {
		return fmt.Errorf("expected depth %d, got %d", expected, gfc.treeDepth)
	}
	return nil
}

func (gfc *goodsFactoryContext) theListShouldContainNodes(expected int) error {
	actual := len(gfc.nodeList)
	if actual != expected {
		return fmt.Errorf("expected %d nodes in list, got %d", expected, actual)
	}
	return nil
}

func (gfc *goodsFactoryContext) theListShouldContain(goodSymbol string) error {
	for _, node := range gfc.nodeList {
		if node.Good == goodSymbol {
			return nil
		}
	}
	return fmt.Errorf("expected list to contain '%s', but it doesn't", goodSymbol)
}

func (gfc *goodsFactoryContext) theRawMaterialsShouldContain(goodSymbol string) error {
	for _, material := range gfc.rawMaterials {
		if material == goodSymbol {
			return nil
		}
	}
	return fmt.Errorf("expected raw materials to contain '%s', but it doesn't", goodSymbol)
}

func (gfc *goodsFactoryContext) theRawMaterialsShouldNotContain(goodSymbol string) error {
	for _, material := range gfc.rawMaterials {
		if material == goodSymbol {
			return fmt.Errorf("expected raw materials not to contain '%s', but it does", goodSymbol)
		}
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeCountShouldBe(expected int) error {
	if gfc.nodeCount != expected {
		return fmt.Errorf("expected node count %d, got %d", expected, gfc.nodeCount)
	}
	return nil
}

func (gfc *goodsFactoryContext) theBUYCountShouldBe(expected int) error {
	if gfc.buyCount != expected {
		return fmt.Errorf("expected BUY count %d, got %d", expected, gfc.buyCount)
	}
	return nil
}

func (gfc *goodsFactoryContext) theFABRICATECountShouldBe(expected int) error {
	if gfc.fabricateCount != expected {
		return fmt.Errorf("expected FABRICATE count %d, got %d", expected, gfc.fabricateCount)
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeShouldBeCompleted() error {
	if !gfc.node.Completed {
		return fmt.Errorf("expected node to be completed, but it isn't")
	}
	return nil
}

func (gfc *goodsFactoryContext) theNodeQuantityAcquiredShouldBe(expected int) error {
	if gfc.node.QuantityAcquired != expected {
		return fmt.Errorf("expected quantity acquired %d, got %d", expected, gfc.node.QuantityAcquired)
	}
	return nil
}

func (gfc *goodsFactoryContext) allChildrenShouldBeCompleted() error {
	if !gfc.allChildrenDone {
		return fmt.Errorf("expected all children to be completed, but they aren't")
	}
	return nil
}

func (gfc *goodsFactoryContext) allChildrenShouldNotBeCompleted() error {
	if gfc.allChildrenDone {
		return fmt.Errorf("expected not all children to be completed, but they are")
	}
	return nil
}

func (gfc *goodsFactoryContext) theEstimatedTimeShouldBeApproximatelyMinutes(expected int) error {
	expectedDuration := time.Duration(expected) * time.Minute
	// Allow 20% tolerance
	tolerance := expectedDuration / 5
	lower := expectedDuration - tolerance
	upper := expectedDuration + tolerance

	if gfc.estimatedDuration < lower || gfc.estimatedDuration > upper {
		return fmt.Errorf("expected estimated time to be approximately %v, got %v", expectedDuration, gfc.estimatedDuration)
	}
	return nil
}

// RegisterGoodsFactorySteps registers all goods factory step definitions
func RegisterGoodsFactorySteps(sc *godog.ScenarioContext) {
	ctx := &goodsFactoryContext{}
	sc.Before(func(bddCtx context.Context, sc *godog.Scenario) (context.Context, error) {
		// Reset context before each scenario
		ctx.reset()
		return bddCtx, nil
	})

	// GoodsFactory setup steps
	sc.Step(`^a goods factory for player (\d+) producing "([^"]*)" in system "([^"]*)"$`, ctx.aGoodsFactoryForPlayerProducingInSystem)
	sc.Step(`^a dependency tree with root "([^"]*)"$`, ctx.aDependencyTreeWithRoot)
	sc.Step(`^factory metadata:$`, ctx.factoryMetadata)
	sc.Step(`^a goods factory in ([A-Z]+) state$`, ctx.aGoodsFactoryInState)
	sc.Step(`^a goods factory with dependency tree of (\d+) nodes$`, ctx.aGoodsFactoryWithDependencyTreeOfNodes)
	sc.Step(`^(\d+) nodes are completed$`, ctx.nodesAreCompleted)

	// GoodsFactory action steps
	sc.Step(`^I create the goods factory$`, ctx.iCreateTheGoodsFactory)
	sc.Step(`^I start the factory$`, ctx.iStartTheFactory)
	sc.Step(`^I attempt to start the factory$`, ctx.iAttemptToStartTheFactory)
	sc.Step(`^I complete the factory$`, ctx.iCompleteTheFactory)
	sc.Step(`^I attempt to complete the factory$`, ctx.iAttemptToCompleteTheFactory)
	sc.Step(`^I fail the factory with error "([^"]*)"$`, ctx.iFailTheFactoryWithError)
	sc.Step(`^I attempt to fail the factory$`, ctx.iAttemptToFailTheFactory)
	sc.Step(`^I stop the factory$`, ctx.iStopTheFactory)
	sc.Step(`^I attempt to stop the factory$`, ctx.iAttemptToStopTheFactory)
	sc.Step(`^I transition factory to ([A-Z]+)$`, ctx.iTransitionFactoryToState)
	sc.Step(`^I set quantity acquired to (\d+)$`, ctx.iSetQuantityAcquiredTo)
	sc.Step(`^I add cost of (\d+) credits$`, ctx.iAddCostOfCredits)
	sc.Step(`^I update metadata with:$`, ctx.iUpdateMetadataWith)
	sc.Step(`^I get the factory progress$`, ctx.iGetTheFactoryProgress)

	// GoodsFactory assertion steps
	sc.Step(`^the goods factory should be valid$`, ctx.theGoodsFactoryShouldBeValid)
	sc.Step(`^the factory player ID should be (\d+)$`, ctx.theFactoryPlayerIDShouldBe)
	sc.Step(`^the factory target good should be "([^"]*)"$`, ctx.theFactoryTargetGoodShouldBe)
	sc.Step(`^the factory system symbol should be "([^"]*)"$`, ctx.theFactorySystemSymbolShouldBe)
	sc.Step(`^the factory status should be "([^"]*)"$`, ctx.theFactoryStatusShouldBe)
	sc.Step(`^the factory started_at timestamp should be set$`, ctx.theFactoryStartedAtTimestampShouldBeSet)
	sc.Step(`^the factory stopped_at timestamp should be set$`, ctx.theFactoryStoppedAtTimestampShouldBeSet)
	sc.Step(`^the factory last error should be "([^"]*)"$`, ctx.theFactoryLastErrorShouldBe)
	sc.Step(`^the factory should be active$`, ctx.theFactoryShouldBeActive)
	sc.Step(`^the factory should be finished$`, ctx.theFactoryShouldBeFinished)
	sc.Step(`^the factory start should fail with error "([^"]*)"$`, ctx.theFactoryStartShouldFailWithError)
	sc.Step(`^the factory complete should fail with error "([^"]*)"$`, ctx.theFactoryCompleteShouldFailWithError)
	sc.Step(`^the factory fail should fail with error "([^"]*)"$`, ctx.theFactoryFailShouldFailWithError)
	sc.Step(`^the factory stop should fail with error "([^"]*)"$`, ctx.theFactoryStopShouldFailWithError)
	sc.Step(`^the factory can be started$`, ctx.theFactoryCanBeStarted)
	sc.Step(`^the factory cannot be started$`, ctx.theFactoryCannotBeStarted)
	sc.Step(`^the factory can be completed$`, ctx.theFactoryCanBeCompleted)
	sc.Step(`^the factory cannot be completed$`, ctx.theFactoryCannotBeCompleted)
	sc.Step(`^the factory can be failed$`, ctx.theFactoryCanBeFailed)
	sc.Step(`^the factory cannot be failed$`, ctx.theFactoryCannotBeFailed)
	sc.Step(`^the factory metadata should contain "([^"]*)" with value "([^"]*)"$`, ctx.theFactoryMetadataShouldContainWithValue)
	sc.Step(`^the factory quantity acquired should be (\d+)$`, ctx.theFactoryQuantityAcquiredShouldBe)
	sc.Step(`^the factory total cost should be (\d+)$`, ctx.theFactoryTotalCostShouldBe)
	sc.Step(`^the progress should be (\d+) percent$`, ctx.theProgressShouldBePercent)

	// SupplyChainNode setup steps
	sc.Step(`^a supply chain node for good "([^"]*)" with acquisition method "([^"]*)"$`, ctx.aSupplyChainNodeForGoodWithAcquisitionMethod)
	sc.Step(`^the node has child "([^"]*)" with acquisition method "([^"]*)"$`, ctx.theNodeHasChildWithAcquisitionMethod)
	sc.Step(`^the node has market activity "([^"]*)"$`, ctx.theNodeHasMarketActivity)
	sc.Step(`^the node has supply level "([^"]*)"$`, ctx.theNodeHasSupplyLevel)
	sc.Step(`^a leaf node for good "([^"]*)"$`, ctx.aLeafNodeForGood)
	sc.Step(`^a supply chain tree:$`, ctx.aSupplyChainTree)
	sc.Step(`^a supply chain tree with depth (\d+)$`, ctx.aSupplyChainTreeWithDepth)
	sc.Step(`^the root node has market activity "([^"]*)"$`, ctx.theRootNodeHasMarketActivity)
	sc.Step(`^node "([^"]*)" is marked completed$`, ctx.nodeIsMarkedCompleted)

	// SupplyChainNode action steps
	sc.Step(`^I create the supply chain node$`, ctx.iCreateTheSupplyChainNode)
	sc.Step(`^I calculate the tree depth$`, ctx.iCalculateTheTreeDepth)
	sc.Step(`^I flatten the tree to a list$`, ctx.iFlattenTheTreeToAList)
	sc.Step(`^I get required raw materials$`, ctx.iGetRequiredRawMaterials)
	sc.Step(`^I count the nodes$`, ctx.iCountTheNodes)
	sc.Step(`^I count by acquisition method$`, ctx.iCountByAcquisitionMethod)
	sc.Step(`^I mark the node completed with quantity (\d+)$`, ctx.iMarkTheNodeCompletedWithQuantity)
	sc.Step(`^I check if all children of "([^"]*)" are completed$`, ctx.iCheckIfAllChildrenOfAreCompleted)
	sc.Step(`^I estimate production time$`, ctx.iEstimateProductionTime)

	// SupplyChainNode assertion steps
	sc.Step(`^the node should be valid$`, ctx.theNodeShouldBeValid)
	sc.Step(`^the node good should be "([^"]*)"$`, ctx.theNodeGoodShouldBe)
	sc.Step(`^the node acquisition method should be "([^"]*)"$`, ctx.theNodeAcquisitionMethodShouldBe)
	sc.Step(`^the node should be a leaf$`, ctx.theNodeShouldBeALeaf)
	sc.Step(`^the node should not be a leaf$`, ctx.theNodeShouldNotBeALeaf)
	sc.Step(`^the node should have (\d+) children$`, ctx.theNodeShouldHaveChildren)
	sc.Step(`^the node market activity should be "([^"]*)"$`, ctx.theNodeMarketActivityShouldBe)
	sc.Step(`^the node supply level should be "([^"]*)"$`, ctx.theNodeSupplyLevelShouldBe)
	sc.Step(`^the depth should be (\d+)$`, ctx.theDepthShouldBe)
	sc.Step(`^the list should contain (\d+) nodes$`, ctx.theListShouldContainNodes)
	sc.Step(`^the list should contain "([^"]*)"$`, ctx.theListShouldContain)
	sc.Step(`^the raw materials should contain "([^"]*)"$`, ctx.theRawMaterialsShouldContain)
	sc.Step(`^the raw materials should not contain "([^"]*)"$`, ctx.theRawMaterialsShouldNotContain)
	sc.Step(`^the node count should be (\d+)$`, ctx.theNodeCountShouldBe)
	sc.Step(`^the BUY count should be (\d+)$`, ctx.theBUYCountShouldBe)
	sc.Step(`^the FABRICATE count should be (\d+)$`, ctx.theFABRICATECountShouldBe)
	sc.Step(`^the node should be completed$`, ctx.theNodeShouldBeCompleted)
	sc.Step(`^the node quantity acquired should be (\d+)$`, ctx.theNodeQuantityAcquiredShouldBe)
	sc.Step(`^all children should be completed$`, ctx.allChildrenShouldBeCompleted)
	sc.Step(`^all children should not be completed$`, ctx.allChildrenShouldNotBeCompleted)
	sc.Step(`^the estimated time should be approximately (\d+) minutes$`, ctx.theEstimatedTimeShouldBeApproximatelyMinutes)
}
