package persistence

// AllModels is the single canonical registry of every persisted model struct.
// AutoMigrate and any test/tooling that needs the full model set must consume
// this slice instead of maintaining a parallel hand-written list, so newly
// added *Model structs cannot silently skip migration.
func AllModels() []any {
	return []any{
		&PlayerModel{},
		&WaypointModel{},
		&ContainerModel{},
		&ContainerLogModel{},
		&ShipModel{},
		&SystemGraphModel{},
		&MarketData{},
		&ContractModel{},
		&GasOperationModel{},
		&StorageOperationModel{},
		&GoodsFactoryModel{},
		&TransactionModel{},
		&MarketPriceHistoryModel{},
		&CaptainEventModel{},
		&ManufacturingPipelineModel{},
		&ManufacturingTaskModel{},
		&ManufacturingTaskDependencyModel{},
		&ManufacturingFactoryStateModel{},
		&EraModel{},
		&SpendReservationModel{},
		&PendingScalingReservationModel{},
		&GateEdgeModel{},
		&TourLegTelemetryModel{},
		&JumpTollSampleModel{},
		&ScoutPostModel{},
		&MarketAbsorptionLedgerModel{},
		&ContractDepotModel{},
		&WarehouseWithdrawalModel{},
		&WarehouseStockingModel{},
		&ShipyardInventoryModel{},
		&SystemCoordModel{},
		&SensingSystemModel{},
		&SensingSlotModel{},
		&SensingSeedHullModel{},
		&SensingChartShareModel{},
		&ScanDedupAllowlistModel{},
		&UnreadableHullModel{},
		&TradeClaimModel{},
		&MVTTransitionModel{},
	}
}
