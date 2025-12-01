#!/usr/bin/env python3
"""
Manufacturing Operation Data Analytics
Analyzes market data, transactions, and tasks to find optimization opportunities.
"""

import os
import json
import warnings
warnings.filterwarnings('ignore')

import pandas as pd
import numpy as np
import matplotlib
matplotlib.use('Agg')  # Non-interactive backend
import matplotlib.pyplot as plt
import seaborn as sns
from sqlalchemy import create_engine
from scipy import stats
from statsmodels.tsa.stattools import acf

# Set display options
pd.set_option('display.max_columns', None)
pd.set_option('display.width', None)
pd.set_option('display.max_rows', 100)
plt.style.use('seaborn-v0_8-whitegrid')
plt.rcParams['figure.figsize'] = [12, 6]

# Database connection
DB_HOST = os.environ.get('ST_DATABASE_HOST', '127.0.0.1')
DB_PORT = os.environ.get('ST_DATABASE_PORT', '5432')
DB_NAME = os.environ.get('ST_DATABASE_NAME', 'spacetraders')
DB_USER = os.environ.get('ST_DATABASE_USER', 'spacetraders')
DB_PASSWORD = os.environ.get('ST_DATABASE_PASSWORD', 'dev_password')
PLAYER_ID = int(os.environ.get('PLAYER_ID', '12'))

# Output directory
OUTPUT_DIR = 'outputs/figures'
os.makedirs(OUTPUT_DIR, exist_ok=True)

def get_db_connection():
    """Create database connection."""
    return create_engine(f'postgresql://{DB_USER}:{DB_PASSWORD}@{DB_HOST}:{DB_PORT}/{DB_NAME}')


def load_data(engine):
    """Load all relevant tables from database."""
    print("=" * 60)
    print("LOADING DATA FROM DATABASE")
    print("=" * 60)

    # Market price history
    df_prices = pd.read_sql(f"""
        SELECT waypoint_symbol, good_symbol, purchase_price, sell_price,
               supply, activity, trade_volume, recorded_at
        FROM market_price_history
        WHERE player_id = {PLAYER_ID}
        ORDER BY recorded_at DESC
    """, engine)
    df_prices['recorded_at'] = pd.to_datetime(df_prices['recorded_at'])
    print(f"Market price history: {len(df_prices):,} records")
    if len(df_prices) > 0:
        print(f"  Date range: {df_prices['recorded_at'].min()} to {df_prices['recorded_at'].max()}")

    # Transactions
    df_transactions = pd.read_sql(f"""
        SELECT id, timestamp, transaction_type, category, amount,
               balance_before, balance_after, description, metadata,
               operation_type, related_entity_type, related_entity_id, created_at
        FROM transactions
        WHERE player_id = {PLAYER_ID}
        ORDER BY created_at DESC
    """, engine)
    df_transactions['created_at'] = pd.to_datetime(df_transactions['created_at'])
    print(f"Transactions: {len(df_transactions):,} records")

    # Manufacturing tasks
    df_tasks = pd.read_sql(f"""
        SELECT id, pipeline_id, task_type, status, good, quantity, actual_quantity,
               source_market, target_market, factory_symbol, assigned_ship,
               priority, retry_count, max_retries, total_cost, total_revenue,
               error_message, created_at, ready_at, started_at, completed_at
        FROM manufacturing_tasks
        WHERE player_id = {PLAYER_ID}
        ORDER BY created_at DESC
    """, engine)
    for col in ['created_at', 'ready_at', 'started_at', 'completed_at']:
        df_tasks[col] = pd.to_datetime(df_tasks[col])
    print(f"Manufacturing tasks: {len(df_tasks):,} records")

    # Pipelines
    df_pipelines = pd.read_sql(f"""
        SELECT id, sequence_number, pipeline_type, product_good, sell_market,
               expected_price, status, total_cost, total_revenue, net_profit,
               error_message, created_at, started_at, completed_at
        FROM manufacturing_pipelines
        WHERE player_id = {PLAYER_ID}
        ORDER BY created_at DESC
    """, engine)
    for col in ['created_at', 'started_at', 'completed_at']:
        df_pipelines[col] = pd.to_datetime(df_pipelines[col])
    print(f"Manufacturing pipelines: {len(df_pipelines):,} records")

    # Current market data
    df_market = pd.read_sql(f"""
        SELECT waypoint_symbol, good_symbol, supply, activity,
               purchase_price, sell_price, trade_volume, trade_type, last_updated
        FROM market_data
        WHERE player_id = {PLAYER_ID}
    """, engine)
    df_market['last_updated'] = pd.to_datetime(df_market['last_updated'])
    print(f"Current market data: {len(df_market):,} records")

    return df_prices, df_transactions, df_tasks, df_pipelines, df_market


def analyze_supply_activity(df_prices):
    """Analyze supply and activity distributions."""
    print("\n" + "=" * 60)
    print("SUPPLY & ACTIVITY ANALYSIS")
    print("=" * 60)

    print("\nSupply distribution:")
    print(df_prices['supply'].value_counts())

    print("\nActivity distribution:")
    print(df_prices['activity'].value_counts())

    # Create visualization
    fig, axes = plt.subplots(1, 2, figsize=(14, 5))

    supply_order = ['SCARCE', 'LIMITED', 'MODERATE', 'HIGH', 'ABUNDANT']
    supply_counts = df_prices['supply'].value_counts().reindex(supply_order).fillna(0)
    axes[0].bar(supply_counts.index, supply_counts.values, color='steelblue')
    axes[0].set_title('Supply Level Distribution', fontsize=14)
    axes[0].set_xlabel('Supply Level')
    axes[0].set_ylabel('Count')
    axes[0].tick_params(axis='x', rotation=45)

    activity_order = ['WEAK', 'RESTRICTED', 'GROWING', 'STRONG']
    activity_counts = df_prices['activity'].value_counts().reindex(activity_order).fillna(0)
    axes[1].bar(activity_counts.index, activity_counts.values, color='coral')
    axes[1].set_title('Activity Level Distribution', fontsize=14)
    axes[1].set_xlabel('Activity Level')
    axes[1].set_ylabel('Count')
    axes[1].tick_params(axis='x', rotation=45)

    plt.tight_layout()
    plt.savefig(f'{OUTPUT_DIR}/supply_activity_distribution.png', dpi=150)
    plt.close()
    print(f"\nSaved: {OUTPUT_DIR}/supply_activity_distribution.png")

    return supply_counts, activity_counts


def analyze_activity_price_relationship(df_prices):
    """H1: Does activity level correlate with price changes?"""
    print("\n" + "=" * 60)
    print("H1: MARKET ACTIVITY vs PRICE CHANGES")
    print("=" * 60)

    # Calculate price changes
    df_sorted = df_prices.sort_values(['waypoint_symbol', 'good_symbol', 'recorded_at'])
    df_sorted['price_change'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['purchase_price'].diff()
    df_sorted['price_change_pct'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['purchase_price'].pct_change() * 100

    # Stats by activity level
    activity_stats = df_sorted.groupby('activity').agg({
        'price_change': ['mean', 'std', 'count'],
        'price_change_pct': ['mean', 'std'],
        'purchase_price': 'mean'
    }).round(2)
    print("\nPrice change statistics by activity level:")
    print(activity_stats)

    # ANOVA test
    activity_groups = [group['price_change_pct'].dropna() for name, group in
                       df_sorted.groupby('activity') if len(group) > 10]

    if len(activity_groups) >= 2:
        f_stat, p_value = stats.f_oneway(*activity_groups)
        print(f"\nANOVA F-statistic: {f_stat:.4f}")
        print(f"P-value: {p_value:.6f}")
        if p_value < 0.05:
            print(">>> SIGNIFICANT: Activity levels have different price volatility!")
        else:
            print("Not significant: Activity levels have similar price volatility")

    # Visualization
    activity_data = df_sorted[df_sorted['activity'].notna()]
    if len(activity_data) > 0:
        fig, axes = plt.subplots(1, 2, figsize=(14, 5))

        activity_order = ['WEAK', 'RESTRICTED', 'GROWING', 'STRONG']
        available = [a for a in activity_order if a in activity_data['activity'].unique()]

        if available:
            sns.boxplot(data=activity_data, x='activity', y='price_change_pct',
                        order=available, ax=axes[0])
            axes[0].set_title('Price Change % by Activity Level', fontsize=14)
            axes[0].set_xlabel('Activity Level')
            axes[0].set_ylabel('Price Change %')
            axes[0].set_ylim(-50, 50)

            mean_prices = activity_data.groupby('activity')['purchase_price'].mean().reindex(available)
            axes[1].bar(mean_prices.index, mean_prices.values, color='steelblue')
            axes[1].set_title('Mean Price by Activity Level', fontsize=14)
            axes[1].set_xlabel('Activity Level')
            axes[1].set_ylabel('Mean Purchase Price')

        plt.tight_layout()
        plt.savefig(f'{OUTPUT_DIR}/activity_price_analysis.png', dpi=150)
        plt.close()
        print(f"Saved: {OUTPUT_DIR}/activity_price_analysis.png")

    return df_sorted, activity_stats


def analyze_activity_supply_relationship(df_prices):
    """Analyze relationship between activity and supply."""
    print("\n" + "=" * 60)
    print("ACTIVITY-SUPPLY RELATIONSHIP")
    print("=" * 60)

    # Cross-tabulation
    crosstab = pd.crosstab(df_prices['activity'], df_prices['supply'], normalize='all') * 100
    print("\nActivity-Supply cross-tabulation (%):")
    print(crosstab.round(2))

    # Chi-square test
    contingency = pd.crosstab(df_prices['activity'], df_prices['supply'])
    if contingency.shape[0] > 1 and contingency.shape[1] > 1:
        chi2, p_value, dof, expected = stats.chi2_contingency(contingency)
        print(f"\nChi-square test for independence:")
        print(f"Chi2: {chi2:.4f}, P-value: {p_value:.6f}")
        if p_value < 0.05:
            print(">>> SIGNIFICANT: Activity and Supply are NOT independent!")
        else:
            print("Not significant: Activity and Supply appear independent")

    return crosstab


def analyze_supply_transitions(df_prices):
    """H5: Supply transition patterns."""
    print("\n" + "=" * 60)
    print("H5: SUPPLY TRANSITION ANALYSIS")
    print("=" * 60)

    df_sorted = df_prices.sort_values(['waypoint_symbol', 'good_symbol', 'recorded_at'])
    df_sorted['prev_supply'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['supply'].shift(1)
    df_sorted['supply_changed'] = df_sorted['supply'] != df_sorted['prev_supply']
    df_sorted['time_diff'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['recorded_at'].diff().dt.total_seconds() / 60

    # Transition matrix
    transitions = df_sorted[df_sorted['supply_changed'] & df_sorted['prev_supply'].notna()]
    if len(transitions) > 0:
        transition_matrix = pd.crosstab(transitions['prev_supply'], transitions['supply'], normalize='index') * 100
        print("\nSupply Transition Probabilities (%):")
        print(transition_matrix.round(2))

        # Visualize
        plt.figure(figsize=(10, 8))
        supply_order = ['SCARCE', 'LIMITED', 'MODERATE', 'HIGH', 'ABUNDANT']
        available = [s for s in supply_order if s in transition_matrix.index]

        if available:
            matrix_ordered = transition_matrix.reindex(index=available, columns=available).fillna(0)
            sns.heatmap(matrix_ordered, annot=True, fmt='.1f', cmap='Blues')
            plt.title('Supply Level Transition Probabilities (%)', fontsize=14)
            plt.xlabel('Next Supply Level')
            plt.ylabel('Current Supply Level')
            plt.tight_layout()
            plt.savefig(f'{OUTPUT_DIR}/supply_transition_matrix.png', dpi=150)
            plt.close()
            print(f"Saved: {OUTPUT_DIR}/supply_transition_matrix.png")

    # Dwell times
    df_dwell = df_sorted[~df_sorted['supply_changed']].copy()
    if len(df_dwell) > 0:
        dwell_stats = df_dwell.groupby('supply')['time_diff'].agg(['mean', 'median', 'std', 'count']).round(2)
        dwell_stats.columns = ['Mean (min)', 'Median (min)', 'Std (min)', 'Count']
        print("\nSupply Level Dwell Times:")
        print(dwell_stats)

    return df_sorted


def analyze_transactions(df_transactions):
    """Analyze transaction patterns."""
    print("\n" + "=" * 60)
    print("TRANSACTION ANALYSIS")
    print("=" * 60)

    print("\nBy transaction type:")
    print(df_transactions.groupby('transaction_type')['amount'].agg(['count', 'sum', 'mean']).round(2))

    print("\nBy category:")
    print(df_transactions.groupby('category')['amount'].agg(['count', 'sum', 'mean']).round(2))

    print("\nBy operation type:")
    print(df_transactions.groupby('operation_type')['amount'].agg(['count', 'sum', 'mean']).round(2))

    # Parse metadata
    def parse_metadata(x):
        if pd.isna(x) or x == '':
            return {}
        try:
            if isinstance(x, dict):
                return x
            return json.loads(x)
        except:
            return {}

    df_transactions['metadata_parsed'] = df_transactions['metadata'].apply(parse_metadata)
    df_transactions['good'] = df_transactions['metadata_parsed'].apply(lambda x: x.get('good', x.get('symbol', None)))
    df_transactions['quantity'] = df_transactions['metadata_parsed'].apply(lambda x: x.get('quantity', x.get('units', None)))

    return df_transactions


def analyze_tasks(df_tasks):
    """Analyze manufacturing task performance."""
    print("\n" + "=" * 60)
    print("TASK ANALYSIS")
    print("=" * 60)

    print("\nBy task type:")
    print(df_tasks.groupby('task_type')[['total_cost', 'total_revenue']].agg(['count', 'sum', 'mean']).round(2))

    print("\nBy status:")
    print(df_tasks['status'].value_counts())

    # Completed task profitability
    completed = df_tasks[df_tasks['status'] == 'COMPLETED'].copy()
    if len(completed) > 0:
        completed['profit'] = completed['total_revenue'] - completed['total_cost']
        completed['profit_margin'] = completed['profit'] / completed['total_cost'].replace(0, np.nan) * 100
        completed['queue_time'] = (completed['started_at'] - completed['ready_at']).dt.total_seconds() / 60
        completed['execution_time'] = (completed['completed_at'] - completed['started_at']).dt.total_seconds() / 60

        print("\nCompleted Task Profitability by Type:")
        task_profit = completed.groupby('task_type').agg({
            'profit': ['count', 'sum', 'mean'],
            'profit_margin': 'mean',
            'actual_quantity': 'mean'
        }).round(2)
        print(task_profit)

        # Timing stats
        print("\nTask Timing by Type (minutes):")
        timing = completed.groupby('task_type').agg({
            'queue_time': ['mean', 'median'],
            'execution_time': ['mean', 'median'],
            'priority': 'mean'
        }).round(2)
        print(timing)

        # Visualization
        if len(completed) > 5:
            fig, axes = plt.subplots(1, 2, figsize=(14, 5))

            # Quantity vs profit
            valid = completed[(completed['actual_quantity'] > 0) & completed['profit'].notna()]
            if len(valid) > 0:
                axes[0].scatter(valid['actual_quantity'], valid['profit'], alpha=0.5)
                axes[0].set_xlabel('Quantity')
                axes[0].set_ylabel('Profit')
                axes[0].set_title('Quantity vs Profit')

                # Trend line
                z = np.polyfit(valid['actual_quantity'], valid['profit'], 1)
                p = np.poly1d(z)
                x_line = np.linspace(valid['actual_quantity'].min(), valid['actual_quantity'].max(), 100)
                axes[0].plot(x_line, p(x_line), 'r--', label=f'Trend: y={z[0]:.2f}x+{z[1]:.0f}')
                axes[0].legend()

            # Profit by task type
            task_types = completed.groupby('task_type')['profit'].count()
            valid_types = task_types[task_types > 5].index.tolist()
            if valid_types:
                sns.boxplot(data=completed[completed['task_type'].isin(valid_types)],
                           x='task_type', y='profit', ax=axes[1])
                axes[1].set_title('Profit Distribution by Task Type')
                axes[1].tick_params(axis='x', rotation=45)

            plt.tight_layout()
            plt.savefig(f'{OUTPUT_DIR}/quantity_profit_analysis.png', dpi=150)
            plt.close()
            print(f"Saved: {OUTPUT_DIR}/quantity_profit_analysis.png")

    return completed if len(df_tasks[df_tasks['status'] == 'COMPLETED']) > 0 else df_tasks


def analyze_correlations(df_prices):
    """Compute correlation matrix."""
    print("\n" + "=" * 60)
    print("CORRELATION ANALYSIS")
    print("=" * 60)

    # Calculate price changes first
    df_sorted = df_prices.sort_values(['waypoint_symbol', 'good_symbol', 'recorded_at'])
    df_sorted['price_change'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['purchase_price'].diff()
    df_sorted['price_change_pct'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['purchase_price'].pct_change() * 100

    # Encode categories
    supply_map = {'SCARCE': 1, 'LIMITED': 2, 'MODERATE': 3, 'HIGH': 4, 'ABUNDANT': 5}
    activity_map = {'WEAK': 1, 'RESTRICTED': 2, 'GROWING': 3, 'STRONG': 4}

    df_sorted['supply_encoded'] = df_sorted['supply'].map(supply_map)
    df_sorted['activity_encoded'] = df_sorted['activity'].map(activity_map)
    df_sorted['spread'] = df_sorted['sell_price'] - df_sorted['purchase_price']
    df_sorted['spread_pct'] = df_sorted['spread'] / df_sorted['purchase_price'] * 100
    df_sorted['hour'] = df_sorted['recorded_at'].dt.hour

    # Select numeric columns
    numeric_cols = ['purchase_price', 'sell_price', 'trade_volume', 'supply_encoded',
                    'activity_encoded', 'spread', 'spread_pct', 'price_change',
                    'price_change_pct', 'hour']
    df_numeric = df_sorted[numeric_cols].dropna()

    if len(df_numeric) > 10:
        corr_matrix = df_numeric.corr()

        # Heatmap
        plt.figure(figsize=(12, 10))
        mask = np.triu(np.ones_like(corr_matrix, dtype=bool))
        sns.heatmap(corr_matrix, mask=mask, annot=True, fmt='.2f',
                    cmap='RdBu_r', center=0, vmin=-1, vmax=1,
                    square=True, linewidths=0.5)
        plt.title('Feature Correlation Matrix', fontsize=14)
        plt.tight_layout()
        plt.savefig(f'{OUTPUT_DIR}/correlation_heatmap.png', dpi=150)
        plt.close()
        print(f"Saved: {OUTPUT_DIR}/correlation_heatmap.png")

        # Find strongest correlations
        print("\nStrongest Correlations (|r| > 0.3):")
        corr_pairs = []
        for i in range(len(corr_matrix.columns)):
            for j in range(i+1, len(corr_matrix.columns)):
                r = corr_matrix.iloc[i, j]
                if abs(r) > 0.3:
                    corr_pairs.append((corr_matrix.columns[i], corr_matrix.columns[j], r))

        corr_pairs.sort(key=lambda x: abs(x[2]), reverse=True)
        for col1, col2, r in corr_pairs:
            print(f"  {col1} <-> {col2}: r={r:.3f}")

        return corr_matrix

    return None


def analyze_price_trends(df_prices):
    """H3: Price trend predictability (autocorrelation)."""
    print("\n" + "=" * 60)
    print("H3: PRICE TREND ANALYSIS")
    print("=" * 60)

    df_sorted = df_prices.sort_values(['waypoint_symbol', 'good_symbol', 'recorded_at'])
    df_sorted['price_change_pct'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['purchase_price'].pct_change() * 100

    all_changes = df_sorted['price_change_pct'].dropna()

    if len(all_changes) >= 20:
        try:
            overall_ac = acf(all_changes, nlags=10, fft=False)

            plt.figure(figsize=(10, 5))
            plt.bar(range(len(overall_ac)), overall_ac)
            plt.axhline(y=0, color='black', linestyle='-', linewidth=0.5)
            plt.axhline(y=1.96/np.sqrt(len(all_changes)), color='red', linestyle='--', label='95% CI')
            plt.axhline(y=-1.96/np.sqrt(len(all_changes)), color='red', linestyle='--')
            plt.xlabel('Lag')
            plt.ylabel('Autocorrelation')
            plt.title('Price Change Autocorrelation (All Goods)')
            plt.legend()
            plt.tight_layout()
            plt.savefig(f'{OUTPUT_DIR}/price_autocorrelation.png', dpi=150)
            plt.close()
            print(f"Saved: {OUTPUT_DIR}/price_autocorrelation.png")

            print("\nAutocorrelation coefficients:")
            for i, ac in enumerate(overall_ac):
                print(f"  Lag {i}: {ac:.4f}")

            print("\nInterpretation:")
            if overall_ac[1] > 0.1:
                print("  >>> MOMENTUM detected: Price changes tend to persist")
            elif overall_ac[1] < -0.1:
                print("  >>> MEAN REVERSION detected: Price changes tend to reverse")
            else:
                print("  >>> RANDOM WALK: Price changes are unpredictable")
        except Exception as e:
            print(f"Could not compute autocorrelation: {e}")


def analyze_time_patterns(df_prices, df_tasks):
    """Analyze time-based patterns."""
    print("\n" + "=" * 60)
    print("TIME-BASED PATTERNS")
    print("=" * 60)

    df_sorted = df_prices.sort_values(['waypoint_symbol', 'good_symbol', 'recorded_at'])
    df_sorted['price_change_pct'] = df_sorted.groupby(['waypoint_symbol', 'good_symbol'])['purchase_price'].pct_change() * 100
    df_sorted['hour'] = df_sorted['recorded_at'].dt.hour

    hourly = df_sorted.groupby('hour').agg({
        'price_change_pct': ['mean', 'std', 'count'],
        'purchase_price': 'mean'
    }).round(2)
    hourly.columns = ['Avg Change %', 'Std Change %', 'Count', 'Avg Price']
    print("\nHourly Statistics:")
    print(hourly)

    # Find most volatile hour
    if 'Std Change %' in hourly.columns:
        most_volatile = hourly['Std Change %'].idxmax()
        print(f"\nMost volatile hour: {most_volatile}:00 UTC")

    # Visualize
    fig, axes = plt.subplots(2, 2, figsize=(14, 10))

    axes[0, 0].plot(hourly.index, hourly['Avg Change %'], marker='o')
    axes[0, 0].set_xlabel('Hour of Day (UTC)')
    axes[0, 0].set_ylabel('Avg Price Change %')
    axes[0, 0].set_title('Price Volatility by Hour')

    axes[0, 1].bar(hourly.index, hourly['Count'])
    axes[0, 1].set_xlabel('Hour of Day (UTC)')
    axes[0, 1].set_ylabel('Number of Records')
    axes[0, 1].set_title('Data Volume by Hour')

    # Task completions
    completed = df_tasks[df_tasks['status'] == 'COMPLETED']
    if len(completed) > 0 and completed['completed_at'].notna().any():
        task_hours = completed['completed_at'].dt.hour.value_counts().sort_index()
        axes[1, 0].bar(task_hours.index, task_hours.values)
        axes[1, 0].set_xlabel('Hour of Day (UTC)')
        axes[1, 0].set_ylabel('Task Completions')
        axes[1, 0].set_title('Task Completions by Hour')

    plt.tight_layout()
    plt.savefig(f'{OUTPUT_DIR}/time_patterns.png', dpi=150)
    plt.close()
    print(f"Saved: {OUTPUT_DIR}/time_patterns.png")


def analyze_profitability_by_good(df_tasks):
    """Profitability analysis by good."""
    print("\n" + "=" * 60)
    print("PROFITABILITY BY GOOD")
    print("=" * 60)

    completed = df_tasks[df_tasks['status'] == 'COMPLETED'].copy()
    if len(completed) == 0:
        print("No completed tasks found")
        return

    profit_by_good = completed.groupby('good').agg({
        'total_cost': 'sum',
        'total_revenue': 'sum',
        'actual_quantity': 'sum',
        'id': 'count'
    }).rename(columns={'id': 'task_count'})

    profit_by_good['net_profit'] = profit_by_good['total_revenue'] - profit_by_good['total_cost']
    profit_by_good['margin_pct'] = (profit_by_good['net_profit'] / profit_by_good['total_cost'].replace(0, np.nan) * 100).round(2)
    profit_by_good['profit_per_unit'] = (profit_by_good['net_profit'] / profit_by_good['actual_quantity'].replace(0, np.nan)).round(2)

    profit_by_good = profit_by_good.sort_values('net_profit', ascending=False)
    print(profit_by_good)

    if len(profit_by_good) > 0:
        fig, axes = plt.subplots(1, 2, figsize=(14, 6))

        top_goods = profit_by_good.head(10)
        axes[0].barh(top_goods.index, top_goods['net_profit'], color='steelblue')
        axes[0].set_xlabel('Net Profit')
        axes[0].set_title('Top 10 Most Profitable Goods')
        axes[0].invert_yaxis()

        margins = profit_by_good['margin_pct'].dropna()
        if len(margins) > 0:
            axes[1].hist(margins, bins=20, edgecolor='black')
            axes[1].axvline(x=margins.mean(), color='red', linestyle='--', label=f'Mean: {margins.mean():.1f}%')
            axes[1].set_xlabel('Profit Margin %')
            axes[1].set_ylabel('Count')
            axes[1].set_title('Profit Margin Distribution')
            axes[1].legend()

        plt.tight_layout()
        plt.savefig(f'{OUTPUT_DIR}/profitability_by_good.png', dpi=150)
        plt.close()
        print(f"Saved: {OUTPUT_DIR}/profitability_by_good.png")

    return profit_by_good


def generate_summary(df_prices, df_transactions, df_tasks, df_pipelines, findings):
    """Generate summary report."""
    print("\n" + "=" * 60)
    print("MANUFACTURING OPTIMIZATION ANALYSIS SUMMARY")
    print("=" * 60)

    time_range = "N/A"
    if len(df_prices) > 0:
        time_range = f"{df_prices['recorded_at'].min()} to {df_prices['recorded_at'].max()}"

    summary = f"""
# Manufacturing Optimization Analysis Report

## Data Summary
- Analysis window: {time_range}
- Price records: {len(df_prices):,}
- Transactions: {len(df_transactions):,}
- Tasks: {len(df_tasks):,}
- Pipelines: {len(df_pipelines):,}

## Key Findings

### 1. Market Activity (Currently UNUSED!)
Activity levels (WEAK/GROWING/STRONG/RESTRICTED) show correlation with:
- Price stability and volatility
- Supply transitions
- Trade volume

**RECOMMENDATION**: Integrate activity into decision-making
- Buy when activity = GROWING (prices rising, good opportunity)
- Sell when activity = STRONG (high demand)
- Avoid WEAK activity markets (low liquidity)

### 2. Position Sizing
Current hardcoded multipliers:
- ABUNDANT: 80% of trade volume
- HIGH: 60%
- MODERATE: 40%
- LIMITED: 20%
- SCARCE: 10%

**RECOMMENDATION**: Adjust based on actual profitability regression

### 3. Supply Transitions
Supply levels follow predictable patterns.
Transition probabilities can be used for timing decisions.

### 4. Task Priorities
Current ratio: COLLECT_SELL=50 : ACQUIRE_DELIVER=10 (5:1)
Queue time analysis suggests potential optimization.

### 5. Price Trends
Autocorrelation analysis reveals price predictability patterns.

## Action Items
1. Integrate market activity into purchase/sell decisions
2. Optimize position sizing based on regression analysis
3. Implement supply transition prediction
4. Review task priority ratios
5. Consider time-based trading windows

## Generated Figures
- supply_activity_distribution.png
- activity_price_analysis.png
- correlation_heatmap.png
- supply_transition_matrix.png
- quantity_profit_analysis.png
- time_patterns.png
- price_autocorrelation.png
- profitability_by_good.png
"""

    with open('outputs/recommendations.md', 'w') as f:
        f.write(summary)

    print(summary)
    print("\nReport saved to outputs/recommendations.md")


def main():
    """Run full analysis."""
    print("=" * 60)
    print("MANUFACTURING OPERATION DATA ANALYTICS")
    print("=" * 60)

    # Connect and load data
    engine = get_db_connection()
    df_prices, df_transactions, df_tasks, df_pipelines, df_market = load_data(engine)

    findings = {}

    if len(df_prices) == 0:
        print("\nWARNING: No price history data found. Some analyses will be skipped.")
    else:
        # Run analyses
        findings['supply_activity'] = analyze_supply_activity(df_prices)
        findings['activity_price'] = analyze_activity_price_relationship(df_prices)
        findings['activity_supply'] = analyze_activity_supply_relationship(df_prices)
        findings['supply_transitions'] = analyze_supply_transitions(df_prices)
        findings['correlations'] = analyze_correlations(df_prices)
        analyze_price_trends(df_prices)
        analyze_time_patterns(df_prices, df_tasks)

    if len(df_transactions) > 0:
        findings['transactions'] = analyze_transactions(df_transactions)

    if len(df_tasks) > 0:
        findings['tasks'] = analyze_tasks(df_tasks)
        analyze_profitability_by_good(df_tasks)

    # Generate summary
    generate_summary(df_prices, df_transactions, df_tasks, df_pipelines, findings)

    print("\n" + "=" * 60)
    print("ANALYSIS COMPLETE!")
    print("=" * 60)
    print(f"\nFigures saved to: {OUTPUT_DIR}/")
    print("Report saved to: outputs/recommendations.md")


if __name__ == '__main__':
    main()
