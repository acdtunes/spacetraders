"""Experiment CLI commands for market liquidity testing"""
import argparse
import asyncio

from configuration.container import get_mediator, get_work_queue_repository, get_experiment_repository
from application.trading.commands.market_liquidity_experiment import MarketLiquidityExperimentCommand
from .player_selector import get_player_id_from_args


def liquidity_command(args: argparse.Namespace) -> int:
    """Handle liquidity experiment command"""
    player_id = get_player_id_from_args(args)
    mediator = get_mediator()

    # Parse ship symbols
    ship_symbols = [s.strip() for s in args.ships.split(',')]

    # Parse batch size fractions if provided
    batch_sizes = [0.1, 0.25, 0.5, 1.0]  # Default
    if args.batch_sizes:
        batch_sizes = [float(b.strip()) for b in args.batch_sizes.split(',')]

    command = MarketLiquidityExperimentCommand(
        ship_symbols=ship_symbols,
        player_id=player_id,
        system_symbol=args.system,
        iterations_per_batch=args.iterations,
        batch_size_fractions=batch_sizes
    )

    print(f"Starting multi-ship liquidity experiment...")
    print(f"Fleet: {len(ship_symbols)} ships ({', '.join(ship_symbols)})")
    print(f"System: {args.system}")
    print(f"Batch sizes: {batch_sizes}")
    print(f"Iterations per batch: {args.iterations}")
    print()

    result = asyncio.run(mediator.send_async(command))

    print(f"✓ Experiment started!")
    print(f"Run ID: {result['run_id']}")
    print(f"Total market pairs: {result['total_pairs']}")
    print(f"Goods discovered: {result['goods']}")
    print(f"Worker containers: {len(result['container_ids'])}")
    print()
    print("Monitor progress:")
    print(f"  ./spacetraders experiment status --run-id {result['run_id']}")
    print(f"  ./spacetraders daemon logs {result['container_ids'][0]}")
    print()

    return 0


def status_command(args: argparse.Namespace) -> int:
    """Handle experiment status command"""
    player_id = get_player_id_from_args(args)
    work_queue_repo = get_work_queue_repository()
    experiment_repo = get_experiment_repository()

    # Get queue status
    queue_status = work_queue_repo.get_queue_status(args.run_id)
    ship_progress = work_queue_repo.get_ship_progress(args.run_id)
    transaction_count = experiment_repo.get_transaction_count(args.run_id)

    total_pairs = sum(queue_status.values())
    completed = queue_status.get('COMPLETED', 0)
    in_progress = queue_status.get('CLAIMED', 0)
    pending = queue_status.get('PENDING', 0)
    failed = queue_status.get('FAILED', 0)

    progress_pct = (completed / total_pairs * 100) if total_pairs > 0 else 0

    print(f"Experiment: {args.run_id}")
    print(f"Progress: {completed}/{total_pairs} pairs ({progress_pct:.1f}%)")
    print()

    if ship_progress:
        print("Ship Performance:")
        # Sort by pairs completed (descending)
        sorted_ships = sorted(ship_progress.items(), key=lambda x: x[1], reverse=True)
        for ship, count in sorted_ships:
            ship_pct = (count / completed * 100) if completed > 0 else 0
            bar_length = int(ship_pct / 5)  # 20 chars max
            bar = '█' * bar_length + '░' * (20 - bar_length)
            print(f"  {ship}: {count} pairs {bar} ({ship_pct:.0f}%)")
        print()

    print("Queue Status:")
    print(f"  Completed: {completed}")
    print(f"  In Progress: {in_progress}")
    print(f"  Pending: {pending}")
    if failed > 0:
        print(f"  Failed: {failed}")
    print()

    print(f"Transactions recorded: {transaction_count}")
    print()

    if completed < total_pairs:
        print("⏳ Experiment in progress...")
    elif failed > 0:
        print(f"⚠️  Experiment completed with {failed} failures")
    else:
        print("✅ Experiment completed successfully!")

    return 0


def setup_experiment_parser(subparsers):
    """Setup experiment command parser"""
    experiment_parser = subparsers.add_parser(
        'experiment',
        help='Market liquidity experiments'
    )
    experiment_subparsers = experiment_parser.add_subparsers(dest='experiment_command')

    # Liquidity experiment command
    liquidity_parser = experiment_subparsers.add_parser(
        'liquidity',
        help='Start multi-ship market liquidity experiment'
    )
    liquidity_parser.add_argument(
        '--ships',
        required=True,
        help='Comma-separated list of ship symbols (e.g., HAULER-1,HAULER-2,HAULER-3)'
    )
    liquidity_parser.add_argument(
        '--system',
        required=True,
        help='System symbol to test (e.g., X1-GZ7)'
    )
    liquidity_parser.add_argument(
        '--iterations',
        type=int,
        default=3,
        help='Iterations per batch size (default: 3)'
    )
    liquidity_parser.add_argument(
        '--batch-sizes',
        help='Comma-separated batch size fractions (default: 0.1,0.25,0.5,1.0)'
    )
    liquidity_parser.add_argument('--agent', help='Agent symbol')
    liquidity_parser.add_argument('--player-id', type=int, help='Player ID')
    liquidity_parser.set_defaults(func=liquidity_command)

    # Status command
    status_parser = experiment_subparsers.add_parser(
        'status',
        help='Check experiment status'
    )
    status_parser.add_argument(
        '--run-id',
        required=True,
        help='Experiment run ID'
    )
    status_parser.add_argument('--agent', help='Agent symbol')
    status_parser.add_argument('--player-id', type=int, help='Player ID')
    status_parser.set_defaults(func=status_command)
