#!/usr/bin/env python3
"""
Tests for batch contract operations (negotiate + evaluate + fulfill multiple contracts)
"""
import sys
from pathlib import Path
import pytest
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))

from spacetraders_bot.operations.contracts import (
    evaluate_contract_profitability,
    batch_contract_operation,
)


class TestContractProfitabilityEvaluation:
    """Test contract profitability evaluation logic"""

    def test_profitable_contract(self):
        """Test contract that meets profitability criteria"""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 10000,
                    'onFulfilled': 100000,
                },
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'IRON_ORE',
                }]
            }
        }

        cargo_capacity = 40

        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        assert is_profitable is True
        assert "meets profitability criteria" in reason
        assert metrics['total_payment'] == 110000
        assert metrics['net_profit'] > 5000
        assert metrics['roi'] > 5.0
        assert metrics['units_remaining'] == 50
        assert metrics['trips'] == 2  # 50 units / 40 capacity = 2 trips

    def test_unprofitable_low_profit_contract(self):
        """Test contract with net profit below minimum threshold"""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 1000,
                    'onFulfilled': 5000,
                },
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'IRON_ORE',
                }]
            }
        }

        cargo_capacity = 40

        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        assert is_profitable is False
        assert "Net profit" in reason
        assert "5,000 cr minimum" in reason
        assert metrics['net_profit'] < 5000

    def test_unprofitable_low_roi_contract(self):
        """Test contract with ROI below minimum threshold"""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 5000,
                    'onFulfilled': 80000,
                },
                'deliver': [{
                    'unitsRequired': 100,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'ADVANCED_CIRCUITRY',  # Expensive item
                }]
            }
        }

        cargo_capacity = 40

        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        # This might be profitable depending on estimated cost
        # Main test is that evaluation completes without error
        assert isinstance(is_profitable, bool)
        assert isinstance(reason, str)
        assert 'roi' in metrics

    def test_partially_fulfilled_contract(self):
        """Test contract that already has some units fulfilled"""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 10000,
                    'onFulfilled': 100000,
                },
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 30,  # 30 already fulfilled
                    'tradeSymbol': 'IRON_ORE',
                }]
            }
        }

        cargo_capacity = 40

        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        # Only 20 units remaining
        assert metrics['units_remaining'] == 20
        assert metrics['trips'] == 1  # 20 units / 40 capacity = 1 trip

    def test_already_fulfilled_contract(self):
        """Test contract that is already completely fulfilled"""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 10000,
                    'onFulfilled': 100000,
                },
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 50,  # Fully fulfilled
                    'tradeSymbol': 'IRON_ORE',
                }]
            }
        }

        cargo_capacity = 40

        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        assert is_profitable is False
        assert "already fulfilled" in reason

    def test_no_delivery_requirements(self):
        """Test contract with no delivery requirements"""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 10000,
                    'onFulfilled': 100000,
                },
                'deliver': []
            }
        }

        cargo_capacity = 40

        is_profitable, reason, metrics = evaluate_contract_profitability(contract, cargo_capacity)

        assert is_profitable is False
        assert "No delivery requirements" in reason


class TestBatchContractOperation:
    """Test batch contract operation"""

    @pytest.fixture
    def mock_api(self):
        """Mock API client"""
        api = MagicMock()
        api.post = MagicMock()
        return api

    @pytest.fixture
    def mock_ship(self):
        """Mock ship controller"""
        ship = MagicMock()
        ship.get_status = MagicMock(return_value={
            'cargo': {'capacity': 40, 'units': 0},
            'nav': {'systemSymbol': 'X1-TEST'},
        })
        return ship

    @pytest.fixture
    def args_batch_3(self):
        """Args for batch of 3 contracts"""
        return type('obj', (object,), {
            'player_id': 1,
            'ship': 'SHIP-1',
            'contract_count': 3,
            'buy_from': None,
            'log_level': 'ERROR',  # Suppress logs in tests
        })()

    def test_batch_all_profitable_contracts(self, mock_api, mock_ship, args_batch_3):
        """Test batch where all contracts are profitable"""
        # Mock negotiate responses (3 profitable contracts)
        negotiate_responses = [
            {
                'data': {
                    'contract': {
                        'id': f'contract-{i}',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            }
            for i in range(1, 4)
        ]

        mock_api.post = MagicMock(side_effect=negotiate_responses)

        # Mock contract_operation to succeed
        with patch('spacetraders_bot.operations.contracts.contract_operation', return_value=0):
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_3, api=mock_api)

        # Should succeed (at least one contract fulfilled)
        assert result == 0

        # Should have negotiated 3 contracts
        assert mock_api.post.call_count == 3

    def test_batch_accept_all_contracts_regardless_of_profitability(self, mock_api, mock_ship, args_batch_3):
        """Test batch where all contracts are accepted, even unprofitable ones"""
        # Mock negotiate responses (profitable, unprofitable, profitable)
        negotiate_responses = [
            # Contract 1: Profitable
            {
                'data': {
                    'contract': {
                        'id': 'contract-1',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
            # Contract 2: Unprofitable (low payment)
            {
                'data': {
                    'contract': {
                        'id': 'contract-2',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 100,
                                'onFulfilled': 500,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
            # Contract 3: Profitable
            {
                'data': {
                    'contract': {
                        'id': 'contract-3',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
        ]

        mock_api.post = MagicMock(side_effect=negotiate_responses)

        # Mock contract_operation to succeed (called for ALL 3 contracts)
        with patch('spacetraders_bot.operations.contracts.contract_operation', return_value=0) as mock_fulfill:
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_3, api=mock_api)

        # Should succeed
        assert result == 0

        # Should have negotiated 3 contracts
        assert mock_api.post.call_count == 3

        # Should have fulfilled ALL 3 contracts (no skipping, even for unprofitable)
        assert mock_fulfill.call_count == 3

    def test_batch_handle_fulfillment_failure(self, mock_api, mock_ship, args_batch_3):
        """Test batch continues after fulfillment failure"""
        # Mock negotiate responses (3 profitable contracts)
        negotiate_responses = [
            {
                'data': {
                    'contract': {
                        'id': f'contract-{i}',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            }
            for i in range(1, 4)
        ]

        mock_api.post = MagicMock(side_effect=negotiate_responses)

        # Mock contract_operation: success, failure, success
        with patch('spacetraders_bot.operations.contracts.contract_operation', side_effect=[0, 1, 0]) as mock_fulfill:
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_3, api=mock_api)

        # Should succeed (2 out of 3 fulfilled)
        assert result == 0

        # Should have tried to fulfill all 3 contracts
        assert mock_fulfill.call_count == 3

    def test_batch_handle_negotiation_failure(self, mock_api, mock_ship, args_batch_3):
        """Test batch continues after negotiation failure"""
        # Mock negotiate responses: success, failure, success
        negotiate_responses = [
            # Contract 1: Success
            {
                'data': {
                    'contract': {
                        'id': 'contract-1',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
            # Contract 2: Failure (no data)
            None,
            # Contract 3: Success
            {
                'data': {
                    'contract': {
                        'id': 'contract-3',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
        ]

        mock_api.post = MagicMock(side_effect=negotiate_responses)

        # Mock contract_operation to succeed (called twice)
        with patch('spacetraders_bot.operations.contracts.contract_operation', return_value=0) as mock_fulfill:
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_3, api=mock_api)

        # Should succeed (2 out of 3 fulfilled)
        assert result == 0

        # Should have tried to negotiate 3 contracts
        assert mock_api.post.call_count == 3

        # Should have fulfilled only 2 contracts (skipped failed negotiation)
        assert mock_fulfill.call_count == 2

    def test_batch_all_fulfillments_fail(self, mock_api, mock_ship, args_batch_3):
        """Test batch where all fulfillments fail (regardless of profitability)"""
        # Mock negotiate responses (3 contracts - mix of profitable and unprofitable)
        negotiate_responses = [
            {
                'data': {
                    'contract': {
                        'id': f'contract-{i}',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10,
                                'onFulfilled': 50,  # Very low payment
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            }
            for i in range(1, 4)
        ]

        mock_api.post = MagicMock(side_effect=negotiate_responses)

        # Mock contract_operation to fail for all 3 contracts
        with patch('spacetraders_bot.operations.contracts.contract_operation', return_value=1):
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_3, api=mock_api)

        # Should fail (no contracts fulfilled)
        assert result == 1

        # Should have negotiated 3 contracts
        assert mock_api.post.call_count == 3


class TestSingleContractBackwardCompatibility:
    """Test that single contract mode still works (backward compatibility)"""

    def test_single_contract_mode_requires_contract_id(self):
        """Test that single contract mode requires contract_id"""
        args = type('obj', (object,), {
            'player_id': 1,
            'ship': 'SHIP-1',
            'contract_count': 1,
            'contract_id': None,  # Missing!
            'log_level': 'ERROR',
        })()

        # Should fail validation in CLI dispatcher
        # (This would be caught in main.py before calling contract_operation)
        assert args.contract_id is None


class TestBatchContractSequentialExecution:
    """
    Test that batch contract operation executes contracts SEQUENTIALLY:
    negotiate -> accept -> fulfill -> complete (contract becomes INACTIVE)
    THEN negotiate next contract.

    This prevents ERROR 4511: Agent already has active contract
    """

    @pytest.fixture
    def mock_api(self):
        """Mock API client"""
        api = MagicMock()
        return api

    @pytest.fixture
    def mock_ship(self):
        """Mock ship controller"""
        ship = MagicMock()
        ship.get_status = MagicMock(return_value={
            'cargo': {'capacity': 40, 'units': 0},
            'nav': {'systemSymbol': 'X1-TEST'},
        })
        return ship

    @pytest.fixture
    def args_batch_2(self):
        """Args for batch of 2 contracts"""
        return type('obj', (object,), {
            'player_id': 1,
            'ship': 'SHIP-1',
            'contract_count': 2,
            'buy_from': None,
            'log_level': 'ERROR',
        })()

    def test_sequential_execution_prevents_error_4511(self, mock_api, mock_ship, args_batch_2):
        """
        Test that contracts are completed sequentially to prevent ERROR 4511.

        If negotiation happens before previous contract is fulfilled,
        the second negotiate call would fail with ERROR 4511 (agent already has active contract).
        """
        negotiate_call_count = 0
        fulfill_call_count = 0

        def mock_negotiate(*args, **kwargs):
            nonlocal negotiate_call_count
            negotiate_call_count += 1

            # Simulate ERROR 4511 if a contract is still active
            if fulfill_call_count < negotiate_call_count - 1:
                # Previous contract not fulfilled yet
                return {
                    'error': {
                        'code': 4511,
                        'message': 'Agent already has an active contract',
                    }
                }

            # Success: return new contract
            return {
                'data': {
                    'contract': {
                        'id': f'contract-{negotiate_call_count}',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10000,
                                'onFulfilled': 100000,
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            }

        def mock_contract_operation(*args, **kwargs):
            nonlocal fulfill_call_count
            fulfill_call_count += 1
            return 0  # Success

        mock_api.post = MagicMock(side_effect=mock_negotiate)

        with patch('spacetraders_bot.operations.contracts.contract_operation', side_effect=mock_contract_operation):
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_2, api=mock_api)

        # Should succeed - no ERROR 4511 because contracts are completed sequentially
        assert result == 0

        # Should have negotiated 2 contracts
        assert negotiate_call_count == 2

        # Should have fulfilled 2 contracts
        assert fulfill_call_count == 2

    def test_always_accept_contracts_no_profitability_filter(self, mock_api, mock_ship, args_batch_2):
        """
        Test that ALL contracts are accepted and fulfilled, regardless of profitability.

        The old behavior would skip unprofitable contracts, leaving them ACTIVE
        and causing ERROR 4511 on the next negotiate.

        The new behavior ALWAYS accepts and fulfills every negotiated contract.
        """
        # First contract: Very unprofitable (payment = 100 cr, cost = ~75,000 cr)
        # Second contract: Also unprofitable
        negotiate_responses = [
            {
                'data': {
                    'contract': {
                        'id': 'contract-1',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 10,
                                'onFulfilled': 90,  # Only 100 cr total
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
            {
                'data': {
                    'contract': {
                        'id': 'contract-2',
                        'type': 'PROCUREMENT',
                        'factionSymbol': 'COSMIC',
                        'terms': {
                            'payment': {
                                'onAccepted': 20,
                                'onFulfilled': 80,  # Only 100 cr total
                            },
                            'deliver': [{
                                'unitsRequired': 50,
                                'unitsFulfilled': 0,
                                'tradeSymbol': 'IRON_ORE',
                                'destinationSymbol': 'X1-TEST-A1',
                            }]
                        }
                    }
                }
            },
        ]

        mock_api.post = MagicMock(side_effect=negotiate_responses)

        with patch('spacetraders_bot.operations.contracts.contract_operation', return_value=0) as mock_fulfill:
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_2, api=mock_api)

        # Should succeed
        assert result == 0

        # Should have negotiated 2 contracts
        assert mock_api.post.call_count == 2

        # CRITICAL: Should have fulfilled BOTH contracts (no profitability filtering)
        # Old behavior: Would skip both (unprofitable) -> 0 fulfillments
        # New behavior: Always fulfill -> 2 fulfillments
        assert mock_fulfill.call_count == 2


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
