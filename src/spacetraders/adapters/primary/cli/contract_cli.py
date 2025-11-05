"""Contract CLI commands"""
import argparse
import asyncio

from ....configuration.container import get_mediator
from ....application.contracts.queries.list_contracts import ListContractsQuery
from ....application.contracts.queries.get_active_contracts import GetActiveContractsQuery
from ....application.contracts.queries.get_contract import GetContractQuery
from ....application.contracts.commands.accept_contract import AcceptContractCommand
from ....application.contracts.commands.deliver_contract import DeliverContractCommand
from ....application.contracts.commands.fulfill_contract import FulfillContractCommand
from ....application.contracts.commands.negotiate_contract import NegotiateContractCommand
from ....application.contracts.commands.batch_contract_workflow import BatchContractWorkflowCommand
from .player_selector import get_player_id_from_args


def list_contracts_command(args: argparse.Namespace) -> int:
    """Handle contract list command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    query = ListContractsQuery(player_id=player_id)

    contracts = asyncio.run(mediator.send_async(query))

    if not contracts:
        print("No contracts found")
        return 0

    print(f"Contracts ({len(contracts)}):")
    for contract in contracts:
        status = "✓ FULFILLED" if contract.fulfilled else ("✓ ACCEPTED" if contract.accepted else "○ OFFERED")
        print(f"  [{contract.contract_id}] {status} - {contract.faction_symbol}")
        print(f"    Type: {contract.type}")
        print(f"    Deliveries: {len(contract.terms.deliveries)}")
        for delivery in contract.terms.deliveries:
            progress = f"{delivery.units_fulfilled}/{delivery.units_required}"
            print(f"      - {delivery.trade_symbol} to {delivery.destination.symbol}: {progress}")

    return 0


def active_contracts_command(args: argparse.Namespace) -> int:
    """Handle active contracts command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    query = GetActiveContractsQuery(player_id=player_id)

    contracts = asyncio.run(mediator.send_async(query))

    if not contracts:
        print("No active contracts")
        return 0

    print(f"Active contracts ({len(contracts)}):")
    for contract in contracts:
        print(f"  [{contract.contract_id}] {contract.faction_symbol}")
        print(f"    Type: {contract.type}")
        print(f"    Deadline: {contract.terms.deadline.isoformat()}")
        for delivery in contract.terms.deliveries:
            remaining = delivery.remaining()
            print(f"      - {delivery.trade_symbol}: {remaining} units remaining")

    return 0


def accept_contract_command(args: argparse.Namespace) -> int:
    """Handle contract accept command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    command = AcceptContractCommand(
        contract_id=args.contract_id,
        player_id=player_id
    )

    try:
        contract = asyncio.run(mediator.send_async(command))
        print(f"✅ Accepted contract {contract.contract_id}")
        print(f"   Payment on accepted: {contract.terms.payment.on_accepted} credits")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def deliver_contract_command(args: argparse.Namespace) -> int:
    """Handle contract deliver command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    command = DeliverContractCommand(
        contract_id=args.contract_id,
        ship_symbol=args.ship_symbol,
        trade_symbol=args.trade_symbol,
        units=args.units,
        player_id=player_id
    )

    try:
        contract = asyncio.run(mediator.send_async(command))
        print(f"✅ Delivered {args.units} units of {args.trade_symbol}")
        # Show updated progress
        for delivery in contract.terms.deliveries:
            if delivery.trade_symbol == args.trade_symbol:
                progress = f"{delivery.units_fulfilled}/{delivery.units_required}"
                print(f"   Progress: {progress} units")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def fulfill_contract_command(args: argparse.Namespace) -> int:
    """Handle contract fulfill command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    command = FulfillContractCommand(
        contract_id=args.contract_id,
        player_id=player_id
    )

    try:
        contract = asyncio.run(mediator.send_async(command))
        print(f"✅ Fulfilled contract {contract.contract_id}")
        print(f"   Payment on fulfilled: {contract.terms.payment.on_fulfilled} credits")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def negotiate_contract_command(args: argparse.Namespace) -> int:
    """Handle contract negotiate command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    command = NegotiateContractCommand(
        ship_symbol=args.ship_symbol,
        player_id=player_id
    )

    try:
        contract = asyncio.run(mediator.send_async(command))
        print(f"✅ Negotiated new contract {contract.contract_id}")
        print(f"   Faction: {contract.faction_symbol}")
        print(f"   Type: {contract.type}")
        print(f"   Deliveries: {len(contract.terms.deliveries)}")
        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def batch_workflow_command(args: argparse.Namespace) -> int:
    """Handle batch contract workflow command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()
    command = BatchContractWorkflowCommand(
        ship_symbol=args.ship_symbol,
        iterations=args.count,
        player_id=player_id
    )

    try:
        print(f"🚀 Starting batch contract workflow for {args.ship_symbol}")
        print(f"   Iterations: {args.count}")
        print()

        result = asyncio.run(mediator.send_async(command))

        print("=" * 50)
        print("📊 Batch Workflow Results")
        print("=" * 50)
        print(f"  Contracts negotiated: {result.negotiated}")
        print(f"  Contracts accepted:   {result.accepted}")
        print(f"  Contracts fulfilled:  {result.fulfilled}")
        print(f"  Contracts failed:     {result.failed}")
        print(f"  Total profit:         {result.total_profit:,} credits")
        print(f"  Total trips:          {result.total_trips}")
        print("=" * 50)

        if result.fulfilled > 0:
            avg_profit = result.total_profit // result.fulfilled
            print(f"  Average profit/contract: {avg_profit:,} credits")

        return 0
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1


def setup_contract_commands(subparsers):
    """Setup contract CLI commands"""
    contract_parser = subparsers.add_parser("contract", help="Contract management")
    contract_subparsers = contract_parser.add_subparsers(dest="contract_command")

    # List command
    list_parser = contract_subparsers.add_parser("list", help="List all contracts")
    list_parser.add_argument("--player-id", type=int)
    list_parser.add_argument("--agent", dest="agent_symbol")
    list_parser.set_defaults(func=list_contracts_command)

    # Active command
    active_parser = contract_subparsers.add_parser("active", help="List active contracts")
    active_parser.add_argument("--player-id", type=int)
    active_parser.add_argument("--agent", dest="agent_symbol")
    active_parser.set_defaults(func=active_contracts_command)

    # Accept command
    accept_parser = contract_subparsers.add_parser("accept", help="Accept a contract")
    accept_parser.add_argument("--contract-id", dest="contract_id", required=True)
    accept_parser.add_argument("--player-id", type=int)
    accept_parser.add_argument("--agent", dest="agent_symbol")
    accept_parser.set_defaults(func=accept_contract_command)

    # Deliver command
    deliver_parser = contract_subparsers.add_parser("deliver", help="Deliver cargo for contract")
    deliver_parser.add_argument("--contract-id", dest="contract_id", required=True)
    deliver_parser.add_argument("--ship", dest="ship_symbol", required=True)
    deliver_parser.add_argument("--trade", dest="trade_symbol", required=True)
    deliver_parser.add_argument("--units", type=int, required=True)
    deliver_parser.add_argument("--player-id", type=int)
    deliver_parser.add_argument("--agent", dest="agent_symbol")
    deliver_parser.set_defaults(func=deliver_contract_command)

    # Fulfill command
    fulfill_parser = contract_subparsers.add_parser("fulfill", help="Fulfill a contract")
    fulfill_parser.add_argument("--contract-id", dest="contract_id", required=True)
    fulfill_parser.add_argument("--player-id", type=int)
    fulfill_parser.add_argument("--agent", dest="agent_symbol")
    fulfill_parser.set_defaults(func=fulfill_contract_command)

    # Negotiate command
    negotiate_parser = contract_subparsers.add_parser("negotiate", help="Negotiate new contract")
    negotiate_parser.add_argument("--ship", dest="ship_symbol", required=True)
    negotiate_parser.add_argument("--player-id", type=int)
    negotiate_parser.add_argument("--agent", dest="agent_symbol")
    negotiate_parser.set_defaults(func=negotiate_contract_command)

    # Batch workflow command
    batch_parser = contract_subparsers.add_parser("batch", help="Execute batch contract workflow")
    batch_parser.add_argument("--ship", dest="ship_symbol", required=True, help="Ship symbol to use")
    batch_parser.add_argument("--count", type=int, default=1, help="Number of contracts to process (default: 1)")
    batch_parser.add_argument("--player-id", type=int)
    batch_parser.add_argument("--agent", dest="agent_symbol")
    batch_parser.set_defaults(func=batch_workflow_command)
