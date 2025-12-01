#!/usr/bin/env python3
"""
Market Dynamics Analysis for Manufacturing Operations
=====================================================

Analyzes 27.7 hours of manufacturing data in system X1-FB5 for player ID 14.
Separates manufacturing signal from market noise and answers key questions:
1. What input supply/activity levels raise output supply?
2. Does the good type make a difference?
3. How long does it take to raise supply levels?
4. Does activity level matter?

Usage:
    python market_dynamics_analysis.py

Or in Jupyter:
    from market_dynamics_analysis import run_full_analysis
    results = run_full_analysis()
"""

import os
import json
import warnings
from datetime import timedelta
from typing import Dict, List, Tuple, Optional

warnings.filterwarnings('ignore')

import pandas as pd
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import seaborn as sns
from sqlalchemy import create_engine, text
from scipy import stats
from statsmodels.formula.api import ols
from statsmodels.stats.multicomp import pairwise_tukeyhsd
from statsmodels.stats.anova import anova_lm

# Set display options
pd.set_option('display.max_columns', None)
pd.set_option('display.width', None)
pd.set_option('display.max_rows', 100)
plt.style.use('seaborn-v0_8-whitegrid')
plt.rcParams['figure.figsize'] = [14, 8]
plt.rcParams['font.size'] = 11

# Database connection
DB_HOST = os.environ.get('ST_DATABASE_HOST', '127.0.0.1')
DB_PORT = os.environ.get('ST_DATABASE_PORT', '5432')
DB_NAME = os.environ.get('ST_DATABASE_NAME', 'spacetraders')
DB_USER = os.environ.get('ST_DATABASE_USER', 'spacetraders')
DB_PASSWORD = os.environ.get('ST_DATABASE_PASSWORD', 'dev_password')
PLAYER_ID = int(os.environ.get('PLAYER_ID', '14'))
SYSTEM = 'X1-FB5'

# Output directory
OUTPUT_DIR = 'outputs'
FIGURES_DIR = os.path.join(OUTPUT_DIR, 'figures')
os.makedirs(FIGURES_DIR, exist_ok=True)

# Supply chain mappings from supply_chain.go
SUPPLY_CHAIN = {
    'DRUGS': ['AMMONIA_ICE', 'POLYNUCLEOTIDES'],
    'CLOTHING': ['FABRICS'],
    'FABRICS': ['FERTILIZERS'],
    'MEDICINE': ['FABRICS', 'POLYNUCLEOTIDES'],
    'JEWELRY': ['GOLD', 'SILVER', 'PRECIOUS_STONES', 'DIAMONDS'],
    'ASSAULT_RIFLES': ['ALUMINUM', 'AMMUNITION'],
    'FIREARMS': ['IRON', 'AMMUNITION'],
    'AMMUNITION': ['IRON', 'LIQUID_NITROGEN'],
    'MACHINERY': ['IRON'],
    'EQUIPMENT': ['ALUMINUM', 'PLASTICS'],
    'ELECTRONICS': ['SILICON_CRYSTALS', 'COPPER'],
    'MICROPROCESSORS': ['SILICON_CRYSTALS', 'COPPER'],
    'SHIP_PARTS': ['EQUIPMENT', 'ELECTRONICS'],
    'FOOD': ['FERTILIZERS'],
}

# Ordinal mappings
SUPPLY_ORDINAL = {'SCARCE': 1, 'LIMITED': 2, 'MODERATE': 3, 'HIGH': 4, 'ABUNDANT': 5}
ACTIVITY_ORDINAL = {'WEAK': 1, 'RESTRICTED': 2, 'GROWING': 3, 'STRONG': 4}


def get_db_connection():
    """Create database connection."""
    return create_engine(f'postgresql://{DB_USER}:{DB_PASSWORD}@{DB_HOST}:{DB_PORT}/{DB_NAME}')


# =============================================================================
# PHASE 1: DATA EXTRACTION
# =============================================================================

def load_market_data(engine) -> pd.DataFrame:
    """Load market price history data."""
    query = text(f"""
        SELECT
            waypoint_symbol, good_symbol, purchase_price, sell_price,
            supply, activity, trade_volume, recorded_at
        FROM market_price_history
        WHERE player_id = {PLAYER_ID}
        AND waypoint_symbol LIKE '{SYSTEM}%%'
        ORDER BY recorded_at
    """)
    with engine.connect() as conn:
        df = pd.read_sql(query, conn)
    df['recorded_at'] = pd.to_datetime(df['recorded_at'], utc=True)
    df['supply_ordinal'] = df['supply'].map(SUPPLY_ORDINAL)
    df['activity_ordinal'] = df['activity'].map(ACTIVITY_ORDINAL)
    return df


def load_container_logs(engine) -> pd.DataFrame:
    """Load container logs for manufacturing events."""
    query = text(f"""
        SELECT
            container_id, timestamp, level, message, metadata
        FROM container_logs
        WHERE player_id = {PLAYER_ID}
        AND container_id LIKE '%%{SYSTEM}%%'
        ORDER BY timestamp
    """)
    with engine.connect() as conn:
        df = pd.read_sql(query, conn)
    df['timestamp'] = pd.to_datetime(df['timestamp'], utc=True)
    return df


def load_transactions(engine) -> pd.DataFrame:
    """Load transaction data for cargo trades."""
    query = text(f"""
        SELECT
            id, timestamp, transaction_type, category, amount,
            balance_before, balance_after, description, metadata,
            operation_type, related_entity_type, related_entity_id
        FROM transactions
        WHERE player_id = {PLAYER_ID}
        ORDER BY timestamp
    """)
    with engine.connect() as conn:
        df = pd.read_sql(query, conn)
    df['timestamp'] = pd.to_datetime(df['timestamp'], utc=True)
    return df


def load_factory_states(engine) -> pd.DataFrame:
    """Load manufacturing factory states."""
    query = text(f"""
        SELECT
            factory_symbol, output_good, current_supply, previous_supply,
            all_inputs_delivered, ready_for_collection,
            required_inputs, delivered_inputs,
            created_at, inputs_completed_at, ready_at
        FROM manufacturing_factory_states
        WHERE player_id = {PLAYER_ID}
        AND factory_symbol LIKE '{SYSTEM}%%'
        ORDER BY created_at
    """)
    with engine.connect() as conn:
        df = pd.read_sql(query, conn)
    for col in ['created_at', 'inputs_completed_at', 'ready_at']:
        df[col] = pd.to_datetime(df[col], utc=True)
    return df


def load_all_data() -> Dict[str, pd.DataFrame]:
    """Load all data from database."""
    print("=" * 70)
    print("LOADING DATA FROM DATABASE")
    print("=" * 70)

    engine = get_db_connection()

    data = {
        'market': load_market_data(engine),
        'logs': load_container_logs(engine),
        'transactions': load_transactions(engine),
        'factory_states': load_factory_states(engine),
    }

    print(f"\nMarket price history: {len(data['market']):,} records")
    if len(data['market']) > 0:
        print(f"  Date range: {data['market']['recorded_at'].min()} to {data['market']['recorded_at'].max()}")
        print(f"  Unique goods: {data['market']['good_symbol'].nunique()}")
        print(f"  Unique waypoints: {data['market']['waypoint_symbol'].nunique()}")

    print(f"\nContainer logs: {len(data['logs']):,} records")
    print(f"Factory states: {len(data['factory_states']):,} records")
    print(f"Transactions: {len(data['transactions']):,} records")

    return data


# =============================================================================
# PHASE 2: SIGNAL VS NOISE DETECTION
# =============================================================================

def calculate_supply_transitions(df: pd.DataFrame) -> pd.DataFrame:
    """Calculate supply level transitions over time."""
    # Sort and calculate previous values
    df_sorted = df.sort_values(['waypoint_symbol', 'good_symbol', 'recorded_at'])

    df_sorted['prev_supply'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['supply'].shift(1)
    df_sorted['prev_supply_ordinal'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['supply_ordinal'].shift(1)
    df_sorted['prev_time'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['recorded_at'].shift(1)

    # Filter to only supply changes
    transitions = df_sorted[df_sorted['supply'] != df_sorted['prev_supply']].copy()
    transitions['supply_change'] = transitions['supply_ordinal'] - transitions['prev_supply_ordinal']
    transitions['time_since_prev'] = (transitions['recorded_at'] - transitions['prev_time']).dt.total_seconds() / 60  # minutes

    return transitions


def identify_manufacturing_events(logs: pd.DataFrame) -> pd.DataFrame:
    """Extract manufacturing-related events from container logs."""
    # Filter to relevant messages
    mfg_keywords = ['supply', 'delivered', 'collect', 'factory', 'sold', 'inputs']
    mask = logs['message'].str.lower().str.contains('|'.join(mfg_keywords), na=False)
    mfg_logs = logs[mask].copy()

    # Parse metadata if present
    def parse_metadata(m):
        if pd.isna(m):
            return {}
        try:
            return json.loads(m)
        except:
            return {}

    mfg_logs['metadata_parsed'] = mfg_logs['metadata'].apply(parse_metadata)

    return mfg_logs


def classify_supply_changes(transitions: pd.DataFrame, mfg_events: pd.DataFrame,
                           window_minutes: int = 5) -> pd.DataFrame:
    """Classify supply changes as manufacturing-driven vs natural drift."""

    if len(mfg_events) == 0:
        transitions['change_type'] = 'natural_drift'
        return transitions

    # For each transition, check if there's a manufacturing event within the window
    transitions = transitions.copy()
    transitions['change_type'] = 'natural_drift'

    for idx, row in transitions.iterrows():
        event_time = row['recorded_at']
        waypoint = row['waypoint_symbol']
        good = row['good_symbol']

        # Look for events in the time window
        time_lower = event_time - timedelta(minutes=window_minutes)
        time_upper = event_time + timedelta(minutes=1)  # Small buffer after

        nearby_events = mfg_events[
            (mfg_events['timestamp'] >= time_lower) &
            (mfg_events['timestamp'] <= time_upper)
        ]

        if len(nearby_events) > 0:
            # Check if any event mentions this waypoint or good
            for _, event in nearby_events.iterrows():
                msg = str(event['message']).lower()
                meta = event.get('metadata_parsed', {})

                if waypoint.lower() in msg or good.lower() in msg:
                    transitions.at[idx, 'change_type'] = 'manufacturing_driven'
                    break
                elif isinstance(meta, dict):
                    if meta.get('factory', '').startswith(SYSTEM) or meta.get('output', '') == good:
                        transitions.at[idx, 'change_type'] = 'manufacturing_driven'
                        break

    return transitions


def analyze_signal_vs_noise(data: Dict[str, pd.DataFrame]) -> Dict:
    """Analyze manufacturing signal vs market noise."""
    print("\n" + "=" * 70)
    print("PHASE 2: SIGNAL VS NOISE ANALYSIS")
    print("=" * 70)

    transitions = calculate_supply_transitions(data['market'])
    mfg_events = identify_manufacturing_events(data['logs'])

    print(f"\nTotal supply transitions detected: {len(transitions)}")
    print(f"Manufacturing events in logs: {len(mfg_events)}")

    # Classify transitions
    classified = classify_supply_changes(transitions, mfg_events)

    # Summary statistics
    change_types = classified['change_type'].value_counts()
    print(f"\nSupply change classification:")
    for ct, count in change_types.items():
        pct = count / len(classified) * 100
        print(f"  {ct}: {count} ({pct:.1f}%)")

    # Analyze by direction
    print(f"\nSupply change direction:")
    direction_counts = classified.groupby(['change_type', classified['supply_change'].apply(
        lambda x: 'increase' if x > 0 else ('decrease' if x < 0 else 'unchanged')
    )]).size()
    print(direction_counts)

    # Time between changes
    print(f"\nTime between supply changes (minutes):")
    print(f"  Mean: {classified['time_since_prev'].mean():.1f}")
    print(f"  Median: {classified['time_since_prev'].median():.1f}")
    print(f"  Std: {classified['time_since_prev'].std():.1f}")

    # Natural drift rate
    natural = classified[classified['change_type'] == 'natural_drift']
    if len(natural) > 0:
        time_span = (classified['recorded_at'].max() - classified['recorded_at'].min()).total_seconds() / 3600
        drift_rate = len(natural) / time_span if time_span > 0 else 0
        print(f"\nNatural drift rate: {drift_rate:.2f} changes/hour")

    return {
        'transitions': classified,
        'mfg_events': mfg_events,
        'change_type_counts': change_types.to_dict(),
    }


# =============================================================================
# PHASE 3: SUPPLY DYNAMICS MODELING
# =============================================================================

def calculate_time_to_supply_raise(data: Dict[str, pd.DataFrame]) -> pd.DataFrame:
    """Calculate time to raise supply from one level to another."""
    print("\n" + "=" * 70)
    print("PHASE 3: SUPPLY DYNAMICS MODELING")
    print("=" * 70)

    market = data['market'].copy()

    # Group by waypoint and good
    results = []

    for (waypoint, good), group in market.groupby(['waypoint_symbol', 'good_symbol']):
        group = group.sort_values('recorded_at')

        if len(group) < 2:
            continue

        # Find sequences where supply increases
        prev_supply = None
        prev_time = None
        sequence_start_time = None
        sequence_start_supply = None

        for _, row in group.iterrows():
            current_supply = row['supply_ordinal']
            current_time = row['recorded_at']

            if pd.isna(current_supply):
                continue

            if prev_supply is not None:
                # Check if supply increased
                if current_supply > prev_supply:
                    if sequence_start_time is None:
                        sequence_start_time = prev_time
                        sequence_start_supply = prev_supply

                    # Record transition
                    time_minutes = (current_time - sequence_start_time).total_seconds() / 60

                    results.append({
                        'waypoint': waypoint,
                        'good': good,
                        'from_supply': SUPPLY_ORDINAL.get(row.get('prev_supply'), prev_supply),
                        'to_supply': current_supply,
                        'from_supply_name': row.get('prev_supply'),
                        'to_supply_name': row['supply'],
                        'time_minutes': time_minutes,
                        'levels_raised': current_supply - sequence_start_supply,
                        'activity': row['activity'],
                        'timestamp': current_time,
                    })
                else:
                    # Reset sequence
                    sequence_start_time = None
                    sequence_start_supply = None

            prev_supply = current_supply
            prev_time = current_time

    df_results = pd.DataFrame(results)

    if len(df_results) > 0:
        print(f"\nSupply raise events detected: {len(df_results)}")

        # Add good type info
        df_results['is_output'] = df_results['good'].isin(SUPPLY_CHAIN.keys())
        df_results['num_inputs'] = df_results['good'].apply(
            lambda g: len(SUPPLY_CHAIN.get(g, []))
        )

        # Summary by good
        print("\nTime to raise supply by good (minutes):")
        good_summary = df_results.groupby('good').agg({
            'time_minutes': ['count', 'mean', 'std', 'median'],
            'levels_raised': 'mean'
        }).round(1)
        print(good_summary.head(20))

        # Summary by target supply level
        print("\nTime to reach supply level (from any lower level):")
        target_summary = df_results.groupby('to_supply_name').agg({
            'time_minutes': ['count', 'mean', 'std', 'median']
        }).round(1)
        print(target_summary)

    return df_results


def analyze_input_supply_impact(data: Dict[str, pd.DataFrame], supply_dynamics: pd.DataFrame) -> Dict:
    """Analyze how input supply levels affect output supply raise times."""
    print("\n" + "-" * 50)
    print("INPUT SUPPLY IMPACT ANALYSIS")
    print("-" * 50)

    market = data['market']

    # For each output good, correlate with input supply levels
    results = []

    for output_good, inputs in SUPPLY_CHAIN.items():
        # Get supply raise events for this output
        output_events = supply_dynamics[supply_dynamics['good'] == output_good]

        if len(output_events) == 0:
            continue

        for _, event in output_events.iterrows():
            event_time = event['timestamp']

            # Look up input supply levels at the time of the event
            for input_good in inputs:
                # Find most recent input supply level
                input_data = market[
                    (market['good_symbol'] == input_good) &
                    (market['recorded_at'] <= event_time)
                ].sort_values('recorded_at', ascending=False)

                if len(input_data) > 0:
                    input_supply = input_data.iloc[0]['supply_ordinal']
                    input_supply_name = input_data.iloc[0]['supply']
                    input_activity = input_data.iloc[0]['activity']

                    results.append({
                        'output_good': output_good,
                        'input_good': input_good,
                        'input_supply_ordinal': input_supply,
                        'input_supply_name': input_supply_name,
                        'input_activity': input_activity,
                        'output_time_minutes': event['time_minutes'],
                        'output_levels_raised': event['levels_raised'],
                        'output_activity': event['activity'],
                    })

    df_input_impact = pd.DataFrame(results)

    if len(df_input_impact) > 0:
        print(f"\nInput-output correlations: {len(df_input_impact)} data points")

        # Correlation: input supply vs output raise time
        if df_input_impact['input_supply_ordinal'].nunique() > 1:
            corr, p_value = stats.spearmanr(
                df_input_impact['input_supply_ordinal'],
                df_input_impact['output_time_minutes']
            )
            print(f"\nCorrelation (input supply vs output raise time):")
            print(f"  Spearman r = {corr:.3f}, p = {p_value:.4f}")

            if p_value < 0.05:
                direction = "faster" if corr < 0 else "slower"
                print(f"  -> Higher input supply leads to {direction} output supply raise")

        # Compare by input supply level
        print("\nOutput raise time by input supply level (minutes):")
        supply_comparison = df_input_impact.groupby('input_supply_name').agg({
            'output_time_minutes': ['count', 'mean', 'std', 'median']
        }).round(1)
        print(supply_comparison)

        # Statistical test: ABUNDANT vs LIMITED inputs
        abundant = df_input_impact[df_input_impact['input_supply_name'] == 'ABUNDANT']['output_time_minutes']
        limited = df_input_impact[df_input_impact['input_supply_name'] == 'LIMITED']['output_time_minutes']

        if len(abundant) > 2 and len(limited) > 2:
            u_stat, p_value = stats.mannwhitneyu(abundant, limited, alternative='two-sided')
            print(f"\nMann-Whitney U test (ABUNDANT vs LIMITED):")
            print(f"  U = {u_stat:.1f}, p = {p_value:.4f}")
            if p_value < 0.05:
                if abundant.mean() < limited.mean():
                    print(f"  -> ABUNDANT inputs significantly faster ({abundant.mean():.1f} vs {limited.mean():.1f} min)")
                else:
                    print(f"  -> LIMITED inputs significantly faster")

    return {'input_impact': df_input_impact}


# =============================================================================
# PHASE 4: GOOD-TYPE VARIANCE ANALYSIS
# =============================================================================

def analyze_good_type_variance(supply_dynamics: pd.DataFrame) -> Dict:
    """Analyze differences in supply dynamics across good types."""
    print("\n" + "=" * 70)
    print("PHASE 4: GOOD-TYPE VARIANCE ANALYSIS")
    print("=" * 70)

    if len(supply_dynamics) == 0:
        print("No supply dynamics data available.")
        return {}

    # Filter to manufactured goods only
    output_goods = list(SUPPLY_CHAIN.keys())
    mfg_dynamics = supply_dynamics[supply_dynamics['good'].isin(output_goods)].copy()

    if len(mfg_dynamics) == 0:
        print("No manufacturing output goods in supply dynamics.")
        return {}

    print(f"\nAnalyzing {len(mfg_dynamics)} supply raise events for {mfg_dynamics['good'].nunique()} manufactured goods")

    # ANOVA: Do supply raise times differ by good type?
    good_groups = [group['time_minutes'].values for name, group in mfg_dynamics.groupby('good') if len(group) >= 2]

    if len(good_groups) >= 2:
        f_stat, p_value = stats.f_oneway(*good_groups)
        print(f"\nANOVA: Do raise times differ by good type?")
        print(f"  F-statistic = {f_stat:.2f}")
        print(f"  p-value = {p_value:.4f}")

        if p_value < 0.05:
            print(f"  -> Significant difference across goods (reject H0)")

            # Post-hoc Tukey HSD
            if len(mfg_dynamics) >= 10:
                try:
                    tukey = pairwise_tukeyhsd(mfg_dynamics['time_minutes'], mfg_dynamics['good'])
                    print(f"\nPost-hoc Tukey HSD:")
                    print(tukey.summary())
                except Exception as e:
                    print(f"  Could not run Tukey HSD: {e}")
        else:
            print(f"  -> No significant difference (fail to reject H0)")

    # Summary by good type
    print("\nSupply raise time by manufactured good (minutes):")
    good_summary = mfg_dynamics.groupby('good').agg({
        'time_minutes': ['count', 'mean', 'std', 'median', 'min', 'max'],
        'num_inputs': 'first'
    }).round(1)
    print(good_summary)

    # Correlation: number of inputs vs raise time
    if mfg_dynamics['num_inputs'].nunique() > 1:
        corr, p_value = stats.spearmanr(
            mfg_dynamics['num_inputs'],
            mfg_dynamics['time_minutes']
        )
        print(f"\nCorrelation (num inputs vs raise time):")
        print(f"  Spearman r = {corr:.3f}, p = {p_value:.4f}")

    return {
        'mfg_dynamics': mfg_dynamics,
        'good_summary': good_summary,
    }


# =============================================================================
# PHASE 5: PRICE ANALYSIS
# =============================================================================

def analyze_price_dynamics(data: Dict[str, pd.DataFrame]) -> Dict:
    """Analyze price-supply correlations and volatility."""
    print("\n" + "=" * 70)
    print("PHASE 5: PRICE ANALYSIS")
    print("=" * 70)

    market = data['market'].copy()

    # Price-supply correlation per good
    print("\nPrice-Supply Correlation by Good:")
    correlations = []

    for good in market['good_symbol'].unique():
        good_data = market[market['good_symbol'] == good]

        if good_data['supply_ordinal'].notna().sum() < 5:
            continue

        valid_data = good_data.dropna(subset=['supply_ordinal', 'sell_price'])
        if len(valid_data) < 5:
            continue

        corr, p_value = stats.spearmanr(valid_data['supply_ordinal'], valid_data['sell_price'])

        correlations.append({
            'good': good,
            'correlation': corr,
            'p_value': p_value,
            'n': len(valid_data),
            'significant': p_value < 0.05,
            'is_output': good in SUPPLY_CHAIN,
        })

    df_corr = pd.DataFrame(correlations)

    if len(df_corr) > 0:
        # Sort by correlation strength
        df_corr = df_corr.sort_values('correlation')

        print("\nTop negative correlations (higher supply = lower price):")
        print(df_corr[df_corr['correlation'] < 0].head(10)[['good', 'correlation', 'p_value', 'n', 'significant']])

        print("\nTop positive correlations (anomalous):")
        print(df_corr[df_corr['correlation'] > 0].head(5)[['good', 'correlation', 'p_value', 'n', 'significant']])

        # Summary for manufactured goods
        mfg_corr = df_corr[df_corr['is_output']]
        if len(mfg_corr) > 0:
            print(f"\nManufactured goods average correlation: {mfg_corr['correlation'].mean():.3f}")

    # Price volatility
    print("\n" + "-" * 50)
    print("PRICE VOLATILITY ANALYSIS")
    print("-" * 50)

    volatility = []
    for good in market['good_symbol'].unique():
        good_data = market[market['good_symbol'] == good]

        if len(good_data) < 10:
            continue

        sell_prices = good_data['sell_price']

        volatility.append({
            'good': good,
            'mean_price': sell_prices.mean(),
            'std_price': sell_prices.std(),
            'cv': sell_prices.std() / sell_prices.mean() if sell_prices.mean() > 0 else 0,  # Coefficient of variation
            'min_price': sell_prices.min(),
            'max_price': sell_prices.max(),
            'price_range': sell_prices.max() - sell_prices.min(),
            'price_range_pct': (sell_prices.max() - sell_prices.min()) / sell_prices.mean() * 100 if sell_prices.mean() > 0 else 0,
            'n': len(good_data),
            'is_output': good in SUPPLY_CHAIN,
        })

    df_volatility = pd.DataFrame(volatility)

    if len(df_volatility) > 0:
        # Most volatile goods
        df_volatility = df_volatility.sort_values('cv', ascending=False)
        print("\nMost volatile goods (by coefficient of variation):")
        print(df_volatility.head(10)[['good', 'mean_price', 'std_price', 'cv', 'price_range_pct', 'is_output']])

        # Manufactured goods volatility
        mfg_vol = df_volatility[df_volatility['is_output']]
        if len(mfg_vol) > 0:
            print(f"\nManufactured goods average CV: {mfg_vol['cv'].mean():.3f}")
            print(f"Non-manufactured goods average CV: {df_volatility[~df_volatility['is_output']]['cv'].mean():.3f}")

    # Price elasticity (price change per supply level change)
    print("\n" + "-" * 50)
    print("PRICE ELASTICITY ANALYSIS")
    print("-" * 50)

    market_sorted = market.sort_values(['good_symbol', 'waypoint_symbol', 'recorded_at'])
    market_sorted['prev_supply_ordinal'] = market_sorted.groupby(['good_symbol', 'waypoint_symbol'])['supply_ordinal'].shift(1)
    market_sorted['prev_sell_price'] = market_sorted.groupby(['good_symbol', 'waypoint_symbol'])['sell_price'].shift(1)

    # Filter to supply changes
    supply_changes = market_sorted[market_sorted['supply_ordinal'] != market_sorted['prev_supply_ordinal']].copy()
    supply_changes['supply_delta'] = supply_changes['supply_ordinal'] - supply_changes['prev_supply_ordinal']
    supply_changes['price_delta'] = supply_changes['sell_price'] - supply_changes['prev_sell_price']
    supply_changes['price_pct_change'] = supply_changes['price_delta'] / supply_changes['prev_sell_price'] * 100

    # Elasticity per good
    elasticity = []
    for good in supply_changes['good_symbol'].unique():
        good_data = supply_changes[supply_changes['good_symbol'] == good]

        if len(good_data) < 3:
            continue

        valid_data = good_data.dropna(subset=['supply_delta', 'price_pct_change'])

        if len(valid_data) < 3 or valid_data['supply_delta'].std() == 0:
            continue

        # Price elasticity = % price change / supply level change
        avg_elasticity = valid_data['price_pct_change'].mean() / valid_data['supply_delta'].abs().mean()

        elasticity.append({
            'good': good,
            'avg_price_change_pct': valid_data['price_pct_change'].mean(),
            'avg_supply_change': valid_data['supply_delta'].mean(),
            'elasticity': avg_elasticity,
            'n': len(valid_data),
            'is_output': good in SUPPLY_CHAIN,
        })

    df_elasticity = pd.DataFrame(elasticity)

    if len(df_elasticity) > 0:
        df_elasticity = df_elasticity.sort_values('elasticity')
        print("\nPrice elasticity by good (% price change per supply level change):")
        print(df_elasticity.head(15)[['good', 'avg_price_change_pct', 'elasticity', 'n', 'is_output']])

    return {
        'correlations': df_corr,
        'volatility': df_volatility,
        'elasticity': df_elasticity,
    }


# =============================================================================
# PHASE 6: ACTIVITY LEVEL ANALYSIS
# =============================================================================

def analyze_activity_impact(data: Dict[str, pd.DataFrame], supply_dynamics: pd.DataFrame) -> Dict:
    """Analyze how activity levels affect supply dynamics."""
    print("\n" + "=" * 70)
    print("PHASE 6: ACTIVITY LEVEL IMPACT")
    print("=" * 70)

    if len(supply_dynamics) == 0:
        print("No supply dynamics data available.")
        return {}

    # Group by activity level
    print("\nSupply raise time by activity level (minutes):")
    activity_summary = supply_dynamics.groupby('activity').agg({
        'time_minutes': ['count', 'mean', 'std', 'median']
    }).round(1)
    print(activity_summary)

    # Statistical test: WEAK vs GROWING
    weak = supply_dynamics[supply_dynamics['activity'] == 'WEAK']['time_minutes']
    growing = supply_dynamics[supply_dynamics['activity'] == 'GROWING']['time_minutes']

    if len(weak) > 2 and len(growing) > 2:
        u_stat, p_value = stats.mannwhitneyu(weak, growing, alternative='two-sided')
        print(f"\nMann-Whitney U test (WEAK vs GROWING):")
        print(f"  U = {u_stat:.1f}, p = {p_value:.4f}")
        if p_value < 0.05:
            if weak.mean() > growing.mean():
                diff_pct = (weak.mean() - growing.mean()) / weak.mean() * 100
                print(f"  -> GROWING activity is {diff_pct:.1f}% faster than WEAK")
            else:
                print(f"  -> WEAK activity is faster (unexpected)")
        else:
            print(f"  -> No significant difference")

    # Activity level distribution during manufacturing
    market = data['market']
    mfg_goods = list(SUPPLY_CHAIN.keys())
    mfg_market = market[market['good_symbol'].isin(mfg_goods)]

    print("\nActivity level distribution for manufactured goods:")
    activity_dist = mfg_market['activity'].value_counts(normalize=True) * 100
    for activity, pct in activity_dist.items():
        print(f"  {activity}: {pct:.1f}%")

    # Chi-square test: supply transition vs activity
    transitions = calculate_supply_transitions(market)
    if len(transitions) > 10:
        contingency = pd.crosstab(
            transitions['supply_change'].apply(lambda x: 'increase' if x > 0 else 'decrease'),
            transitions['activity']
        )

        if contingency.shape[0] >= 2 and contingency.shape[1] >= 2:
            chi2, p_value, dof, expected = stats.chi2_contingency(contingency)
            print(f"\nChi-square test (supply transition × activity):")
            print(f"  Chi2 = {chi2:.2f}, p = {p_value:.4f}, df = {dof}")
            if p_value < 0.05:
                print(f"  -> Activity level significantly associated with transition direction")

    return {
        'activity_summary': activity_summary,
    }


# =============================================================================
# PHASE 7: STATISTICAL TESTS & REGRESSION
# =============================================================================

def run_regression_analysis(supply_dynamics: pd.DataFrame, input_impact: pd.DataFrame) -> Dict:
    """Run regression models to quantify factors affecting supply raise time."""
    print("\n" + "=" * 70)
    print("PHASE 7: REGRESSION ANALYSIS")
    print("=" * 70)

    results = {}

    # Model 1: Time to raise supply ~ good + activity + levels_raised
    if len(supply_dynamics) >= 20:
        # Prepare data
        model_data = supply_dynamics.dropna(subset=['time_minutes', 'good', 'activity', 'levels_raised'])

        if len(model_data) >= 20:
            try:
                model1 = ols('time_minutes ~ C(good) + C(activity) + levels_raised', data=model_data).fit()

                print("\nModel 1: time_minutes ~ good + activity + levels_raised")
                print(f"  R-squared: {model1.rsquared:.3f}")
                print(f"  Adj R-squared: {model1.rsquared_adj:.3f}")
                print(f"  F-statistic: {model1.fvalue:.2f}, p = {model1.f_pvalue:.4f}")

                print("\nSignificant coefficients (p < 0.05):")
                for name, coef, pval in zip(model1.params.index, model1.params.values, model1.pvalues.values):
                    if pval < 0.05:
                        print(f"  {name}: {coef:.2f} (p = {pval:.4f})")

                results['model1'] = model1
            except Exception as e:
                print(f"  Model 1 failed: {e}")

    # Model 2: Time to raise supply ~ input_supply + activity (for output goods)
    if len(input_impact) >= 15:
        model_data = input_impact.dropna(subset=['output_time_minutes', 'input_supply_ordinal', 'output_activity'])

        if len(model_data) >= 15:
            try:
                model2 = ols('output_time_minutes ~ input_supply_ordinal + C(output_activity)', data=model_data).fit()

                print("\nModel 2: output_time ~ input_supply_level + output_activity")
                print(f"  R-squared: {model2.rsquared:.3f}")
                print(f"  Adj R-squared: {model2.rsquared_adj:.3f}")

                print("\nCoefficients:")
                for name, coef, pval in zip(model2.params.index, model2.params.values, model2.pvalues.values):
                    sig = "*" if pval < 0.05 else ""
                    print(f"  {name}: {coef:.2f} (p = {pval:.4f}) {sig}")

                results['model2'] = model2
            except Exception as e:
                print(f"  Model 2 failed: {e}")

    return results


# =============================================================================
# VISUALIZATIONS
# =============================================================================

def create_visualizations(data: Dict[str, pd.DataFrame], supply_dynamics: pd.DataFrame,
                         price_results: Dict, signal_results: Dict):
    """Create all visualizations."""
    print("\n" + "=" * 70)
    print("CREATING VISUALIZATIONS")
    print("=" * 70)

    # 1. Supply level time series for key goods
    fig, axes = plt.subplots(2, 2, figsize=(16, 12))
    key_goods = ['DRUGS', 'CLOTHING', 'FABRICS', 'MEDICINE']

    market = data['market']

    for ax, good in zip(axes.flatten(), key_goods):
        good_data = market[market['good_symbol'] == good].sort_values('recorded_at')

        if len(good_data) > 0:
            ax.plot(good_data['recorded_at'], good_data['supply_ordinal'], 'b-', alpha=0.7, label='Supply Level')
            ax.set_title(f'{good} Supply Level Over Time')
            ax.set_xlabel('Time')
            ax.set_ylabel('Supply Level (1=SCARCE, 5=ABUNDANT)')
            ax.set_ylim(0.5, 5.5)
            ax.set_yticks([1, 2, 3, 4, 5])
            ax.set_yticklabels(['SCARCE', 'LIMITED', 'MODERATE', 'HIGH', 'ABUNDANT'])
            ax.tick_params(axis='x', rotation=45)

    plt.tight_layout()
    plt.savefig(os.path.join(FIGURES_DIR, 'supply_time_series.png'), dpi=150, bbox_inches='tight')
    plt.close()
    print("  Created: supply_time_series.png")

    # 2. Box plot: raise time by good type
    if len(supply_dynamics) > 0:
        mfg_dynamics = supply_dynamics[supply_dynamics['good'].isin(SUPPLY_CHAIN.keys())]

        if len(mfg_dynamics) > 0:
            fig, ax = plt.subplots(figsize=(14, 8))

            good_order = mfg_dynamics.groupby('good')['time_minutes'].median().sort_values().index.tolist()

            sns.boxplot(data=mfg_dynamics, x='good', y='time_minutes', order=good_order, ax=ax)
            ax.set_title('Time to Raise Supply Level by Good Type')
            ax.set_xlabel('Good')
            ax.set_ylabel('Time (minutes)')
            ax.tick_params(axis='x', rotation=45)

            plt.tight_layout()
            plt.savefig(os.path.join(FIGURES_DIR, 'raise_time_by_good.png'), dpi=150, bbox_inches='tight')
            plt.close()
            print("  Created: raise_time_by_good.png")

    # 3. Supply-Price correlation heatmap
    if 'correlations' in price_results and len(price_results['correlations']) > 0:
        corr_data = price_results['correlations']
        mfg_corr = corr_data[corr_data['is_output']].set_index('good')

        if len(mfg_corr) > 0:
            fig, ax = plt.subplots(figsize=(10, 8))

            sns.barplot(data=mfg_corr.reset_index(), x='correlation', y='good',
                       palette='RdYlGn_r', ax=ax)
            ax.axvline(x=0, color='black', linestyle='--', alpha=0.5)
            ax.set_title('Price-Supply Correlation by Manufactured Good\n(Negative = Higher Supply → Lower Price)')
            ax.set_xlabel('Spearman Correlation')
            ax.set_ylabel('Good')

            plt.tight_layout()
            plt.savefig(os.path.join(FIGURES_DIR, 'price_supply_correlation.png'), dpi=150, bbox_inches='tight')
            plt.close()
            print("  Created: price_supply_correlation.png")

    # 4. Activity level distribution
    if len(market) > 0:
        fig, axes = plt.subplots(1, 2, figsize=(14, 6))

        # Activity distribution
        activity_counts = market['activity'].value_counts()
        axes[0].pie(activity_counts, labels=activity_counts.index, autopct='%1.1f%%', startangle=90)
        axes[0].set_title('Activity Level Distribution (All Markets)')

        # Supply distribution
        supply_counts = market['supply'].value_counts()
        axes[1].pie(supply_counts, labels=supply_counts.index, autopct='%1.1f%%', startangle=90)
        axes[1].set_title('Supply Level Distribution (All Markets)')

        plt.tight_layout()
        plt.savefig(os.path.join(FIGURES_DIR, 'activity_supply_distribution.png'), dpi=150, bbox_inches='tight')
        plt.close()
        print("  Created: activity_supply_distribution.png")

    # 5. Price volatility by good
    if 'volatility' in price_results and len(price_results['volatility']) > 0:
        vol_data = price_results['volatility'].sort_values('cv', ascending=False).head(15)

        fig, ax = plt.subplots(figsize=(12, 8))

        colors = ['coral' if x else 'steelblue' for x in vol_data['is_output']]
        bars = ax.barh(vol_data['good'], vol_data['cv'], color=colors)
        ax.set_title('Price Volatility by Good (Coefficient of Variation)')
        ax.set_xlabel('CV (std/mean)')
        ax.set_ylabel('Good')

        # Legend
        import matplotlib.patches as mpatches
        output_patch = mpatches.Patch(color='coral', label='Manufactured')
        input_patch = mpatches.Patch(color='steelblue', label='Input/Raw')
        ax.legend(handles=[output_patch, input_patch])

        plt.tight_layout()
        plt.savefig(os.path.join(FIGURES_DIR, 'price_volatility.png'), dpi=150, bbox_inches='tight')
        plt.close()
        print("  Created: price_volatility.png")

    # 6. Supply transition heatmap
    if 'transitions' in signal_results and len(signal_results['transitions']) > 0:
        transitions = signal_results['transitions']

        # Create transition matrix
        transition_matrix = pd.crosstab(
            transitions['prev_supply'].fillna('Unknown'),
            transitions['supply'].fillna('Unknown'),
            normalize='index'
        ) * 100

        # Reorder
        supply_order = ['SCARCE', 'LIMITED', 'MODERATE', 'HIGH', 'ABUNDANT']
        available_supplies = [s for s in supply_order if s in transition_matrix.index]

        if len(available_supplies) >= 2:
            transition_matrix = transition_matrix.reindex(index=available_supplies, columns=available_supplies, fill_value=0)

            fig, ax = plt.subplots(figsize=(10, 8))
            sns.heatmap(transition_matrix, annot=True, fmt='.1f', cmap='YlOrRd', ax=ax)
            ax.set_title('Supply Level Transition Probability (%)')
            ax.set_xlabel('To Supply Level')
            ax.set_ylabel('From Supply Level')

            plt.tight_layout()
            plt.savefig(os.path.join(FIGURES_DIR, 'supply_transition_matrix.png'), dpi=150, bbox_inches='tight')
            plt.close()
            print("  Created: supply_transition_matrix.png")

    print(f"\nAll visualizations saved to: {FIGURES_DIR}/")


# =============================================================================
# MAIN ANALYSIS
# =============================================================================

def print_key_findings(results: Dict):
    """Print summary of key findings."""
    print("\n" + "=" * 70)
    print("KEY FINDINGS SUMMARY")
    print("=" * 70)

    findings = []

    # Signal vs noise
    if 'signal' in results and 'change_type_counts' in results['signal']:
        counts = results['signal']['change_type_counts']
        total = sum(counts.values())
        mfg_driven = counts.get('manufacturing_driven', 0)
        pct = mfg_driven / total * 100 if total > 0 else 0
        findings.append(f"1. SIGNAL VS NOISE: {pct:.1f}% of supply changes were manufacturing-driven")

    # Good type differences
    if 'good_variance' in results and 'good_summary' in results['good_variance']:
        summary = results['good_variance']['good_summary']
        if len(summary) > 0:
            try:
                fastest = summary[('time_minutes', 'mean')].idxmin()
                slowest = summary[('time_minutes', 'mean')].idxmax()
                findings.append(f"2. GOOD VARIANCE: Fastest supply raise = {fastest}, Slowest = {slowest}")
            except:
                pass

    # Input supply impact
    if 'input_impact' in results and 'input_impact' in results['input_impact']:
        df = results['input_impact']['input_impact']
        if len(df) > 0:
            corr = df['input_supply_ordinal'].corr(df['output_time_minutes'])
            direction = "faster" if corr < 0 else "slower"
            findings.append(f"3. INPUT SUPPLY: Higher input supply leads to {direction} output production (r={corr:.2f})")

    # Price dynamics
    if 'price' in results and 'correlations' in results['price']:
        corr_df = results['price']['correlations']
        if len(corr_df) > 0:
            avg_corr = corr_df[corr_df['is_output']]['correlation'].mean()
            findings.append(f"4. PRICE-SUPPLY: Manufactured goods show avg correlation of {avg_corr:.2f} (supply vs price)")

    print("\n".join(findings) if findings else "No significant findings to report.")


def run_full_analysis() -> Dict:
    """Run the complete market dynamics analysis."""
    print("\n" + "#" * 70)
    print("# MARKET DYNAMICS ANALYSIS - X1-FB5 MANUFACTURING OPERATION")
    print(f"# Player ID: {PLAYER_ID}")
    print("#" * 70)

    # Load all data
    data = load_all_data()

    results = {'data': data}

    # Phase 2: Signal vs Noise
    results['signal'] = analyze_signal_vs_noise(data)

    # Phase 3: Supply Dynamics
    supply_dynamics = calculate_time_to_supply_raise(data)
    results['supply_dynamics'] = supply_dynamics
    results['input_impact'] = analyze_input_supply_impact(data, supply_dynamics)

    # Phase 4: Good-Type Variance
    results['good_variance'] = analyze_good_type_variance(supply_dynamics)

    # Phase 5: Price Analysis
    results['price'] = analyze_price_dynamics(data)

    # Phase 6: Activity Impact
    results['activity'] = analyze_activity_impact(data, supply_dynamics)

    # Phase 7: Regression Analysis
    input_impact_df = results['input_impact'].get('input_impact', pd.DataFrame())
    results['regression'] = run_regression_analysis(supply_dynamics, input_impact_df)

    # Create visualizations
    create_visualizations(data, supply_dynamics, results['price'], results['signal'])

    # Print key findings
    print_key_findings(results)

    # Export results
    print("\n" + "=" * 70)
    print("EXPORTING RESULTS")
    print("=" * 70)

    # Export supply dynamics
    if len(supply_dynamics) > 0:
        supply_dynamics.to_csv(os.path.join(OUTPUT_DIR, 'supply_dynamics.csv'), index=False)
        print(f"  Exported: supply_dynamics.csv ({len(supply_dynamics)} rows)")

    # Export price correlations
    if 'correlations' in results['price'] and len(results['price']['correlations']) > 0:
        results['price']['correlations'].to_csv(os.path.join(OUTPUT_DIR, 'price_correlations.csv'), index=False)
        print(f"  Exported: price_correlations.csv")

    # Export transitions
    if 'transitions' in results['signal'] and len(results['signal']['transitions']) > 0:
        results['signal']['transitions'].to_csv(os.path.join(OUTPUT_DIR, 'supply_transitions.csv'), index=False)
        print(f"  Exported: supply_transitions.csv")

    print("\n" + "=" * 70)
    print("ANALYSIS COMPLETE")
    print("=" * 70)

    return results


if __name__ == '__main__':
    results = run_full_analysis()
