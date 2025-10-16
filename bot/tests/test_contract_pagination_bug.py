#!/usr/bin/env python3
"""
Test for contract pagination bug causing ERROR 4511.

BUG: batch_contract_operation only checks first page of contracts (limit=10),
missing active contracts on subsequent pages.

SCENARIO:
- Agent has 16 contracts total (meta.total = 16)
- First 10 contracts are fulfilled (page 1)
- 11th-15th contracts are fulfilled (page 2, items 1-5)
- 16th contract is ACTIVE (page 2, item 6): accepted=True, fulfilled=False
- batch_contract_operation checks /my/contracts (defaults to page 1, limit 10)
- Check shows "no active contracts" because page 1 only has fulfilled contracts
- Negotiation fails with ERROR 4511: "Agent already has an active contract"

ROOT CAUSE: GET /my/contracts without pagination parameters only returns first 10 contracts.
FIX: Fetch ALL pages of contracts to check for active contracts.
"""
import sys
from pathlib import Path
import pytest
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))

from spacetraders_bot.operations.contracts import batch_contract_operation


class TestContractPaginationBug:
    """Test that contract pagination is handled correctly"""

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
    def args_batch_1(self):
        """Args for batch of 1 contract"""
        return type('obj', (object,), {
            'player_id': 2,
            'ship': 'STARGAZER-1',
            'contract_count': 1,
            'buy_from': None,
            'log_level': 'ERROR',
        })()

    def test_pagination_bug_active_contract_on_page_2_OLD_BEHAVIOR(self):
        """
        DISABLED: This test documents the OLD BEHAVIOR (bug).

        The fix changes the behavior:
        - OLD: Only checks page 1 → misses active contract on page 2 → ERROR 4511
        - NEW: Checks all pages → finds active contract → fulfills it first

        This test is kept for documentation but not run.
        """
        pass

    def test_pagination_all_pages_fetched(self, mock_api, mock_ship, args_batch_1):
        """
        Test that ALL pages of contracts are fetched when checking for active contracts.

        This is the FIX verification test:
        - Should fetch page 1 (10 contracts)
        - Detect total=16 from meta
        - Fetch page 2 (6 contracts)
        - Find active contract on page 2
        - Fulfill it before negotiating new contract
        """
        # Mock GET /my/contracts with proper pagination support
        page_1_contracts = [
            {'id': f'c{i}', 'accepted': True, 'fulfilled': True, 'terms': {'deliver': []}}
            for i in range(1, 11)
        ]

        page_2_contracts = [
            {'id': f'c{i}', 'accepted': True, 'fulfilled': True, 'terms': {'deliver': []}}
            for i in range(11, 16)
        ]

        active_contract = {
            'id': 'active-contract',
            'accepted': True,
            'fulfilled': False,
            'factionSymbol': 'COSMIC',
            'type': 'PROCUREMENT',
            'terms': {
                'payment': {'onAccepted': 1000, 'onFulfilled': 5000},
                'deliver': [{
                    'tradeSymbol': 'IRON',
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'destinationSymbol': 'X1-TEST-A1',
                }]
            }
        }
        page_2_contracts.append(active_contract)

        get_calls = []

        def mock_get(endpoint):
            """Mock GET with pagination tracking"""
            get_calls.append(endpoint)

            if '/my/contracts' in endpoint:
                if 'page=2' in endpoint:
                    # Return page 2 when requested
                    return {
                        'data': page_2_contracts,
                        'meta': {'total': 16, 'page': 2, 'limit': 20}
                    }
                else:
                    # Return page 1 by default (page=1 or no page param)
                    return {
                        'data': page_1_contracts,
                        'meta': {'total': 16, 'page': 1, 'limit': 20}
                    }
            return None

        fulfill_calls = []

        def mock_fulfill(args, **kwargs):
            """Track which contracts are fulfilled"""
            fulfill_calls.append(args.contract_id)
            return 0  # Success

        mock_api.get = MagicMock(side_effect=mock_get)
        mock_api.post = MagicMock(return_value={
            'data': {
                'contract': {
                    'id': 'new-contract',
                    'type': 'PROCUREMENT',
                    'factionSymbol': 'COSMIC',
                    'terms': {
                        'payment': {'onAccepted': 10000, 'onFulfilled': 100000},
                        'deliver': [{
                            'unitsRequired': 50,
                            'unitsFulfilled': 0,
                            'tradeSymbol': 'IRON',
                            'destinationSymbol': 'X1-TEST-A1',
                        }]
                    }
                }
            }
        })

        with patch('spacetraders_bot.operations.contracts.contract_operation', side_effect=mock_fulfill):
            with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
                result = batch_contract_operation(args_batch_1, api=mock_api)

        # After fix: Should succeed
        assert result == 0

        # CRITICAL: Should have fetched page 2 to find active contract
        # This is the KEY verification that pagination is working
        page_2_requests = [c for c in get_calls if 'page=2' in c]
        assert len(page_2_requests) > 0, f"Page 2 was never fetched - pagination not working! get_calls={get_calls}"

        # Should have fulfilled the active contract from page 2 BEFORE negotiating new one
        assert 'active-contract' in fulfill_calls, f"Active contract not fulfilled! fulfill_calls={fulfill_calls}"
        assert fulfill_calls[0] == 'active-contract', f"Active contract not fulfilled first! fulfill_calls={fulfill_calls}"


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
