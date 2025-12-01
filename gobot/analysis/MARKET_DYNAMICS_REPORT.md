# Market Dynamics Analysis Report
## X1-FB5 Manufacturing Operation - Player ID 14

**Analysis Period:** 2025-11-30 13:58 UTC to 2025-12-01 18:49 UTC (28.85 hours)
**Generated:** 2025-12-01

---

## Executive Summary

This report presents a comprehensive statistical analysis of market dynamics during a 28+ hour manufacturing operation in the SpaceTraders X1-FB5 system. The analysis separates manufacturing-driven supply changes from natural market noise and quantifies the relationships between input supply levels, activity states, good types, and supply raise times.

### Key Findings

| Metric | Value | Statistical Significance |
|--------|-------|-------------------------|
| Manufacturing-driven supply changes | 12.5% of total | - |
| Average time to raise supply | 24.1 minutes | - |
| Input supply impact | r = -0.11 | p = 0.05 (marginal) |
| Good type variance | F = 1.77 | p = 0.05 (marginal) |
| Price-supply correlation | r = -0.90 avg | p < 0.001 (significant) |
| Activity level impact (RESTRICTED) | +12.5 min | p = 0.02 (significant) |

---

## 1. Data Sources and Methodology

### 1.1 Data Sources

| Source | Records | Description |
|--------|---------|-------------|
| `market_price_history` | 5,961 | Market snapshots with supply, activity, prices |
| `container_logs` | 22,311 | Manufacturing operation logs |
| `transactions` | 3,855 | Financial transactions (purchases, sales, refuels) |
| `manufacturing_factory_states` | 18 | Factory delivery state tracking |

### 1.2 Data Scope

- **System:** X1-FB5
- **Waypoints analyzed:** 23
- **Unique goods tracked:** 46
- **Manufactured goods analyzed:** 14

### 1.3 Supply Chain Reference

The analysis uses the SpaceTraders supply chain mappings:

```
Output Good          Input Requirements
─────────────────────────────────────────────────────
DRUGS               ← AMMONIA_ICE, POLYNUCLEOTIDES
CLOTHING            ← FABRICS
FABRICS             ← FERTILIZERS
MEDICINE            ← FABRICS, POLYNUCLEOTIDES
JEWELRY             ← GOLD, SILVER, PRECIOUS_STONES, DIAMONDS
ASSAULT_RIFLES      ← ALUMINUM, AMMUNITION
FIREARMS            ← IRON, AMMUNITION
AMMUNITION          ← IRON, LIQUID_NITROGEN
MACHINERY           ← IRON
EQUIPMENT           ← ALUMINUM, PLASTICS
ELECTRONICS         ← SILICON_CRYSTALS, COPPER
MICROPROCESSORS     ← SILICON_CRYSTALS, COPPER
SHIP_PARTS          ← EQUIPMENT, ELECTRONICS
FOOD                ← FERTILIZERS
```

### 1.4 Ordinal Encodings

**Supply Levels:**
| Level | Ordinal Value |
|-------|---------------|
| SCARCE | 1 |
| LIMITED | 2 |
| MODERATE | 3 |
| HIGH | 4 |
| ABUNDANT | 5 |

**Activity Levels:**
| Level | Ordinal Value |
|-------|---------------|
| WEAK | 1 |
| RESTRICTED | 2 |
| GROWING | 3 |
| STRONG | 4 |

---

## 2. Signal vs Noise Analysis

### 2.1 Objective

Distinguish supply changes caused by manufacturing operations from natural market fluctuations (polling artifacts, background market dynamics).

### 2.2 Methodology

#### 2.2.1 Supply Transition Detection

Supply transitions are identified by comparing consecutive market observations for each (waypoint, good) pair:

```python
transitions[i] = {
    'from_supply': market[i-1].supply,
    'to_supply': market[i].supply,
    'time_delta': market[i].timestamp - market[i-1].timestamp,
    'supply_change': market[i].supply_ordinal - market[i-1].supply_ordinal
}
```

#### 2.2.2 Classification Algorithm

Supply changes are classified using temporal proximity to manufacturing events:

```python
def classify_supply_change(transition, manufacturing_events):
    # Window: 5 minutes before to 2 minutes after
    window_start = transition.timestamp - timedelta(minutes=5)
    window_end = transition.timestamp + timedelta(minutes=2)

    nearby_events = manufacturing_events[
        (manufacturing_events.timestamp >= window_start) &
        (manufacturing_events.timestamp <= window_end)
    ]

    if len(nearby_events) > 0:
        return 'manufacturing_driven'
    else:
        return 'natural_drift'
```

Manufacturing events extracted from container logs:
- "Delivered cargo to factory"
- "Collected from factory"
- "all_inputs_delivered"
- "Factory supply changed"

### 2.3 Results

| Classification | Count | Percentage |
|----------------|-------|------------|
| Natural Drift | 751 | 87.5% |
| Manufacturing-Driven | 107 | 12.5% |
| **Total** | **858** | **100%** |

#### 2.3.1 Supply Change Direction by Classification

| Classification | Increase | Decrease | Unchanged |
|----------------|----------|----------|-----------|
| Manufacturing-Driven | 46 | 59 | 2 |
| Natural Drift | 256 | 377 | 118 |

#### 2.3.2 Time Between Changes

| Statistic | Value (minutes) |
|-----------|-----------------|
| Mean | 24.1 |
| Median | 21.6 |
| Std Dev | 29.5 |
| Natural drift rate | 26.03 changes/hour |

### 2.4 Interpretation

The majority (87.5%) of observed supply changes occur independently of manufacturing activity, representing the natural "noise floor" of market dynamics. This establishes a baseline for identifying manufacturing-driven signals.

---

## 3. Supply Dynamics Modeling

### 3.1 Objective

Quantify how long it takes to raise supply levels for manufactured goods.

### 3.2 Methodology

#### 3.2.1 Supply Raise Event Detection

A supply raise event is defined as a transition where:

```python
supply_raise = (current_supply_ordinal > previous_supply_ordinal)
```

For each raise event, we calculate:

```python
time_to_raise = current_timestamp - previous_timestamp  # in minutes
levels_raised = current_supply_ordinal - previous_supply_ordinal
```

### 3.3 Results

**Total supply raise events detected:** 302

#### 3.3.1 Time to Raise Supply by Target Level

| Target Level | Count | Mean (min) | Std Dev | Median |
|--------------|-------|------------|---------|--------|
| ABUNDANT | 15 | 26.0 | 19.0 | 24.2 |
| HIGH | 138 | 28.6 | 15.7 | 26.9 |
| MODERATE | 77 | 20.4 | 13.8 | 20.2 |
| LIMITED | 72 | 29.1 | 60.7 | 20.4 |

#### 3.3.2 Time to Raise Supply by Manufactured Good

| Good | Count | Mean (min) | Std Dev | Median | Inputs |
|------|-------|------------|---------|--------|--------|
| CLOTHING | 16 | 17.9 | 7.9 | 19.7 | 1 |
| FABRICS | 13 | 19.6 | 8.0 | 22.0 | 1 |
| EQUIPMENT | 19 | 19.1 | 9.8 | 21.6 | 2 |
| JEWELRY | 18 | 22.3 | 12.0 | 20.5 | 4 |
| FIREARMS | 6 | 24.6 | 14.2 | 27.5 | 2 |
| MEDICINE | 8 | 25.1 | 4.8 | 26.3 | 2 |
| ELECTRONICS | 18 | 26.6 | 20.1 | 28.5 | 2 |
| MACHINERY | 8 | 27.1 | 12.8 | 27.0 | 1 |
| ASSAULT_RIFLES | 11 | 27.3 | 14.6 | 26.0 | 2 |
| DRUGS | 11 | 28.4 | 13.2 | 31.8 | 2 |
| MICROPROCESSORS | 6 | 29.0 | 18.9 | 26.4 | 2 |
| AMMUNITION | 4 | 29.6 | 13.0 | 27.9 | 2 |
| FOOD | 4 | 29.6 | 2.1 | 30.3 | 1 |
| SHIP_PARTS | 19 | 34.0 | 15.6 | 34.3 | 2 |

### 3.4 Interpretation

- **Fastest goods:** CLOTHING (17.9 min), FABRICS (19.6 min) - both single-input goods
- **Slowest goods:** SHIP_PARTS (34.0 min), FOOD (29.6 min)
- Single-input goods tend to be faster, but the relationship is not strictly linear

---

## 4. Input Supply Impact Analysis

### 4.1 Objective

Determine whether higher input supply levels lead to faster output supply raises.

### 4.2 Methodology

#### 4.2.1 Input-Output Pairing

For each output supply raise event, we identify the corresponding input goods and their supply levels at the time of the raise:

```python
for output_raise in supply_raises:
    inputs = SUPPLY_CHAIN[output_raise.good]
    for input_good in inputs:
        input_supply = get_supply_at_time(
            input_good,
            output_raise.waypoint,
            output_raise.timestamp
        )
        pairs.append({
            'output_good': output_raise.good,
            'output_time': output_raise.time_minutes,
            'input_good': input_good,
            'input_supply_ordinal': input_supply
        })
```

#### 4.2.2 Correlation Analysis

**Spearman Rank Correlation** is used because:
1. Supply levels are ordinal (not continuous)
2. We expect a monotonic (not necessarily linear) relationship
3. It's robust to outliers

The Spearman correlation coefficient is calculated as:

$$\rho = 1 - \frac{6 \sum d_i^2}{n(n^2 - 1)}$$

Where $d_i$ is the difference between ranks of corresponding values.

### 4.3 Results

**Input-output pairs analyzed:** 317

#### 4.3.1 Spearman Correlation

| Metric | Value |
|--------|-------|
| Spearman r | -0.110 |
| p-value | 0.0496 |
| Interpretation | Marginally significant (p < 0.05) |

**Direction:** Negative correlation indicates that higher input supply is associated with faster (lower) output raise times.

#### 4.3.2 Output Raise Time by Input Supply Level

| Input Supply | Count | Mean (min) | Std Dev | Median |
|--------------|-------|------------|---------|--------|
| SCARCE | 122 | 27.1 | 12.7 | 26.4 |
| LIMITED | 91 | 23.4 | 12.5 | 23.7 |
| MODERATE | 91 | 24.1 | 16.4 | 20.2 |
| HIGH | 7 | 22.3 | 13.9 | 20.2 |
| ABUNDANT | 6 | 30.4 | 8.3 | 28.0 |

#### 4.3.3 Mann-Whitney U Test

**Hypothesis:** ABUNDANT input supply leads to faster output production than LIMITED.

$$H_0: \text{median}_{ABUNDANT} = \text{median}_{LIMITED}$$
$$H_1: \text{median}_{ABUNDANT} \neq \text{median}_{LIMITED}$$

| Statistic | Value |
|-----------|-------|
| U statistic | 374.0 |
| p-value | 0.1323 |
| Conclusion | Fail to reject H₀ |

The Mann-Whitney U test is a non-parametric test that:
- Compares two independent samples
- Does not assume normal distribution
- Tests whether one distribution is stochastically greater than the other

### 4.4 Interpretation

The marginally significant negative correlation (r = -0.11) suggests a weak relationship where higher input supply leads to faster output production. However, the small sample size for ABUNDANT inputs (n=6) limits the power of more specific comparisons.

---

## 5. Good-Type Variance Analysis

### 5.1 Objective

Determine if different manufactured goods have significantly different supply raise times.

### 5.2 Methodology

#### 5.2.1 One-Way ANOVA

Analysis of Variance (ANOVA) tests whether the means of multiple groups differ significantly.

**Model:**
$$Y_{ij} = \mu + \tau_i + \epsilon_{ij}$$

Where:
- $Y_{ij}$ = observation j in group i (time to raise for good type i)
- $\mu$ = overall mean
- $\tau_i$ = effect of good type i
- $\epsilon_{ij}$ = random error ~ N(0, σ²)

**Hypotheses:**
$$H_0: \tau_1 = \tau_2 = ... = \tau_k = 0 \text{ (all goods have same mean)}$$
$$H_1: \text{At least one } \tau_i \neq 0$$

**F-statistic:**
$$F = \frac{MS_{between}}{MS_{within}} = \frac{\sum n_i(\bar{Y}_i - \bar{Y})^2 / (k-1)}{\sum\sum(Y_{ij} - \bar{Y}_i)^2 / (N-k)}$$

### 5.3 Results

#### 5.3.1 ANOVA Results

| Source | SS | df | MS | F | p-value |
|--------|----|----|----|----|---------|
| Between Groups | 4,892.3 | 13 | 376.3 | 1.77 | 0.0529 |
| Within Groups | 31,241.7 | 147 | 212.5 | - | - |
| **Total** | **36,134.0** | **160** | - | - | - |

**Conclusion:** F(13, 147) = 1.77, p = 0.053 - marginally significant at α = 0.05.

#### 5.3.2 Number of Inputs vs Raise Time

**Spearman Correlation:**

| Metric | Value |
|--------|-------|
| Spearman r | 0.088 |
| p-value | 0.265 |
| Interpretation | Not significant |

### 5.4 Interpretation

The ANOVA result (p = 0.053) is borderline significant, suggesting possible differences between goods but not conclusive. The number of input requirements does not significantly predict raise time (r = 0.088, p = 0.265).

---

## 6. Price Analysis

### 6.1 Price-Supply Correlation

#### 6.1.1 Methodology

**Spearman Correlation** between supply_ordinal and sell_price for each good:

$$\rho_{good} = \text{spearman}(\text{supply\_ordinal}, \text{sell\_price})$$

### 6.1.2 Results - Top Correlations

| Good | Correlation | p-value | n | Significant |
|------|-------------|---------|---|-------------|
| ALUMINUM_ORE | -0.954 | 1.75e-31 | 59 | Yes |
| DRUGS | -0.952 | 1.55e-74 | 143 | Yes |
| IRON_ORE | -0.951 | 2.41e-17 | 33 | Yes |
| JEWELRY | -0.950 | 5.62e-70 | 137 | Yes |
| PLATINUM | -0.946 | 7.79e-79 | 159 | Yes |
| FIREARMS | -0.946 | 7.54e-63 | 127 | Yes |
| ASSAULT_RIFLES | -0.942 | 1.94e-62 | 130 | Yes |
| MACHINERY | -0.940 | 3.34e-90 | 191 | Yes |
| COPPER_ORE | -0.938 | 3.25e-17 | 36 | Yes |
| SHIP_PLATING | -0.934 | 2.28e-83 | 184 | Yes |

**Average correlation for manufactured goods:** -0.904

### 6.2 Price Volatility Analysis

#### 6.2.1 Methodology

**Coefficient of Variation (CV)** measures relative variability:

$$CV = \frac{\sigma}{\mu} = \frac{\text{standard deviation}}{\text{mean}}$$

**Price Range Percentage:**
$$\text{Range\%} = \frac{\text{max} - \text{min}}{\text{mean}} \times 100$$

#### 6.2.2 Results - Most Volatile Goods

| Good | Mean Price | Std Dev | CV | Range % | Manufactured |
|------|------------|---------|-----|---------|--------------|
| SHIP_PLATING | 8,895 | 5,146 | 0.579 | 144.6% | No |
| SHIP_PARTS | 9,590 | 5,449 | 0.568 | 139.1% | Yes |
| MICROPROCESSORS | 4,795 | 2,696 | 0.562 | 125.1% | Yes |
| DRUGS | 7,110 | 3,880 | 0.546 | 119.3% | Yes |
| ALUMINUM_ORE | 106 | 56 | 0.528 | 116.0% | No |
| JEWELRY | 4,630 | 2,380 | 0.514 | 114.6% | Yes |
| FIREARMS | 5,519 | 2,836 | 0.514 | 110.3% | Yes |
| AMMUNITION | 2,470 | 1,268 | 0.514 | 107.7% | Yes |
| ADVANCED_CIRCUITRY | 9,080 | 4,616 | 0.508 | 110.9% | No |
| ASSAULT_RIFLES | 5,772 | 2,848 | 0.493 | 104.1% | Yes |

**Average CV for manufactured goods:** 0.470
**Average CV for non-manufactured goods:** 0.266

**Manufactured goods are 1.77x more volatile than raw materials.**

### 6.3 Price Elasticity Analysis

#### 6.3.1 Methodology

Price elasticity measures price sensitivity to supply changes:

$$\text{Elasticity} = \frac{\Delta \text{Price\%}}{\Delta \text{Supply Level}}$$

Calculated as average percentage price change per supply level transition.

#### 6.3.2 Results - Top Elastic Goods

| Good | Avg Price Change % | Elasticity | n | Manufactured |
|------|-------------------|------------|---|--------------|
| AMMONIA_ICE | -1.27% | -1.27 | 16 | No |
| LIQUID_HYDROGEN | -1.17% | -1.17 | 12 | No |
| POLYNUCLEOTIDES | -0.74% | -0.70 | 35 | No |
| FERTILIZERS | -0.68% | -0.68 | 37 | No |
| QUARTZ_SAND | -0.57% | -0.57 | 7 | No |
| DIAMONDS | -0.49% | -0.49 | 9 | No |
| EQUIPMENT | -0.45% | -0.44 | 48 | Yes |
| MEDICINE | -0.40% | -0.40 | 19 | Yes |

### 6.4 Interpretation

- **Strong inverse price-supply relationship** (avg r = -0.90): Higher supply consistently leads to lower prices
- **Manufactured goods are more volatile** (CV 0.47 vs 0.27): Manufacturing creates price instability
- **Raw materials are more price-elastic**: Input prices respond more strongly to supply changes

---

## 7. Activity Level Impact Analysis

### 7.1 Objective

Determine whether market activity levels affect supply raise times.

### 7.2 Methodology

#### 7.2.1 Grouping by Activity

Supply raise events are grouped by the activity level at the time of the raise.

#### 7.2.2 Statistical Tests

**Mann-Whitney U Test** compares WEAK vs GROWING activity:

$$H_0: \text{median}_{WEAK} = \text{median}_{GROWING}$$

**Chi-Square Test of Independence** examines the relationship between activity level and supply transition direction:

$$\chi^2 = \sum \frac{(O_{ij} - E_{ij})^2}{E_{ij}}$$

Where:
- $O_{ij}$ = observed frequency
- $E_{ij}$ = expected frequency under independence

### 7.3 Results

#### 7.3.1 Supply Raise Time by Activity Level

| Activity | Count | Mean (min) | Std Dev | Median |
|----------|-------|------------|---------|--------|
| WEAK | 124 | 20.8 | 15.7 | 19.0 |
| GROWING | 60 | 19.6 | 10.8 | 21.6 |
| STRONG | 12 | 25.6 | 7.8 | 28.5 |
| RESTRICTED | 106 | 37.2 | 49.9 | 30.1 |

#### 7.3.2 Mann-Whitney U Test (WEAK vs GROWING)

| Statistic | Value |
|-----------|-------|
| U statistic | 3,640.0 |
| p-value | 0.8144 |
| Conclusion | No significant difference |

#### 7.3.3 Activity Distribution for Manufactured Goods

| Activity | Percentage |
|----------|------------|
| WEAK | 62.2% |
| RESTRICTED | 27.9% |
| GROWING | 8.0% |
| STRONG | 1.8% |

#### 7.3.4 Chi-Square Test (Activity × Transition Direction)

| Statistic | Value |
|-----------|-------|
| χ² | 22.86 |
| p-value | 0.0001 |
| Degrees of freedom | 4 |
| Conclusion | Significant association |

### 7.4 Interpretation

- **RESTRICTED activity significantly slows supply raises** (37.2 min vs 20.8 min for WEAK)
- **WEAK activity dominates** (62%) - markets are mostly quiet
- **Activity level and supply direction are associated** (χ² = 22.86, p < 0.001)

---

## 8. Regression Analysis

### 8.1 Model 1: Supply Raise Time

#### 8.1.1 Specification

**Ordinary Least Squares (OLS) Regression:**

$$\text{time\_minutes} = \beta_0 + \beta_1 \cdot \text{good} + \beta_2 \cdot \text{activity} + \beta_3 \cdot \text{levels\_raised} + \epsilon$$

Using categorical encoding (dummy variables) for good and activity.

#### 8.1.2 Results

| Metric | Value |
|--------|-------|
| R² | 0.373 |
| Adjusted R² | 0.265 |
| F-statistic | 3.47 |
| Prob(F) | < 0.0001 |

**Significant Coefficients (p < 0.05):**

| Variable | Coefficient | Std Error | t-stat | p-value |
|----------|-------------|-----------|--------|---------|
| Intercept | 37.18 | 16.6 | 2.24 | 0.028 |
| C(good)[QUARTZ_SAND] | 164.48 | 31.9 | 5.16 | <0.001 |
| C(activity)[RESTRICTED] | 12.49 | 5.3 | 2.35 | 0.021 |

#### 8.1.3 Interpretation

- **R² = 0.37**: The model explains 37% of variance in raise times
- **RESTRICTED activity adds ~12.5 minutes** to expected raise time
- **QUARTZ_SAND is an outlier** (+164 min) - likely due to special market dynamics

### 8.2 Model 2: Input-Output Relationship

#### 8.2.1 Specification

$$\text{output\_time} = \beta_0 + \beta_1 \cdot \text{input\_supply\_ordinal} + \beta_2 \cdot \text{output\_activity} + \epsilon$$

#### 8.2.2 Results

| Metric | Value |
|--------|-------|
| R² | 0.157 |
| Adjusted R² | 0.146 |

**Coefficients:**

| Variable | Coefficient | Std Error | t-stat | p-value | Sig |
|----------|-------------|-----------|--------|---------|-----|
| Intercept | 19.62 | 4.0 | 4.90 | <0.001 | * |
| C(activity)[RESTRICTED] | 11.16 | 2.7 | 4.13 | <0.001 | * |
| C(activity)[STRONG] | 4.84 | 3.8 | 1.29 | 0.197 | |
| C(activity)[WEAK] | -0.54 | 2.0 | -0.27 | 0.786 | |
| input_supply_ordinal | 0.44 | 0.8 | 0.58 | 0.563 | |

#### 8.2.3 Interpretation

- **Input supply level is not a significant predictor** when controlling for activity
- **RESTRICTED activity is the dominant factor** (+11.16 min, p < 0.001)

---

## 9. Statistical Methods Summary

### 9.1 Descriptive Statistics

| Method | Purpose | Usage |
|--------|---------|-------|
| Mean, Median, Std Dev | Central tendency and spread | All continuous variables |
| Coefficient of Variation | Relative variability | Price volatility comparison |
| Frequency distributions | Categorical variable analysis | Supply/activity levels |

### 9.2 Correlation Analysis

| Method | Purpose | Assumptions |
|--------|---------|-------------|
| **Spearman Correlation** | Monotonic relationship strength | Ordinal or non-normal data |
| Formula | $\rho = 1 - \frac{6\sum d_i^2}{n(n^2-1)}$ | Ranks instead of raw values |

### 9.3 Hypothesis Testing

| Test | Purpose | Assumptions |
|------|---------|-------------|
| **Mann-Whitney U** | Compare two independent groups | Non-parametric, ordinal OK |
| **One-Way ANOVA** | Compare multiple group means | Normal distribution, equal variance |
| **Chi-Square** | Test independence of categoricals | Expected counts ≥ 5 |

### 9.4 Regression Analysis

| Method | Purpose | Assumptions |
|--------|---------|-------------|
| **OLS Regression** | Model continuous outcome | Linearity, normality of residuals |
| **Categorical Encoding** | Handle factor variables | Reference category comparison |

---

## 10. Conclusions and Recommendations

### 10.1 Key Insights

1. **Signal Detection:** Only 12.5% of market changes are manufacturing-driven; the rest is noise that should be filtered in optimization algorithms.

2. **Supply Raise Times:** Expect 18-34 minutes to raise supply one level, depending on good type:
   - **Fast (18-22 min):** CLOTHING, FABRICS, EQUIPMENT
   - **Medium (22-28 min):** JEWELRY, MEDICINE, ELECTRONICS, DRUGS
   - **Slow (28-34 min):** SHIP_PARTS, FOOD, AMMUNITION

3. **Activity Level Impact:** RESTRICTED activity significantly delays supply raises (+12.5 min). Prioritize manufacturing when markets show WEAK or GROWING activity.

4. **Price Dynamics:** Strong inverse price-supply correlation (r = -0.90) confirms that raising supply will lower sell prices. Manufactured goods are 1.77x more volatile than raw materials.

5. **Input Supply:** While higher input supply shows a weak correlation with faster output (r = -0.11), the effect is not statistically robust when controlling for activity level.

### 10.2 Recommendations

1. **Timing:** Prefer manufacturing when market activity is WEAK or GROWING; avoid RESTRICTED periods.

2. **Good Selection:** For fastest supply raises, focus on single-input goods (CLOTHING, FABRICS).

3. **Price Management:** Monitor supply levels to avoid oversupply; ABUNDANT supply significantly reduces sell prices.

4. **Noise Filtering:** Implement a 5-minute smoothing window in market monitoring to filter natural drift from manufacturing signals.

### 10.3 Limitations

1. **Single system analysis** - Results may not generalize to other systems
2. **28-hour window** - Longer observation periods may reveal different patterns
3. **No other players** - Pure manufacturing dynamics without competition effects
4. **Small samples for some goods** - ABUNDANT input supply had only 6 observations

---

## Appendix A: Generated Artifacts

### Data Exports
- `outputs/supply_dynamics.csv` - 302 supply raise events
- `outputs/supply_transitions.csv` - 858 transition records
- `outputs/price_correlations.csv` - Price-supply correlations by good

### Visualizations
- `outputs/figures/supply_time_series.png` - Multi-good supply timeline
- `outputs/figures/raise_time_by_good.png` - Box plots by good type
- `outputs/figures/price_supply_correlation.png` - Correlation heatmap
- `outputs/figures/activity_supply_distribution.png` - Activity breakdown
- `outputs/figures/price_volatility.png` - Volatility comparison
- `outputs/figures/supply_transition_matrix.png` - State transitions

### Code
- `market_dynamics_analysis.py` - Main analysis script (700+ lines)
- `market_dynamics.ipynb` - Interactive Jupyter notebook

---

## Appendix B: Mathematical Notation Reference

| Symbol | Meaning |
|--------|---------|
| $\rho$ | Spearman correlation coefficient |
| $\mu$ | Population/sample mean |
| $\sigma$ | Standard deviation |
| $n$ | Sample size |
| $F$ | F-statistic (ANOVA) |
| $\chi^2$ | Chi-square statistic |
| $U$ | Mann-Whitney U statistic |
| $R^2$ | Coefficient of determination |
| $\beta$ | Regression coefficient |
| $H_0$ | Null hypothesis |
| $H_1$ | Alternative hypothesis |
| $p$ | p-value (significance level) |
| $\alpha$ | Significance threshold (typically 0.05) |

---

*Report generated by market_dynamics_analysis.py*
*Analysis framework: pandas, scipy, statsmodels*
